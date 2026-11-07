package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	carpoolSyncInterval = time.Minute
	// 订阅变更回调可能早于外层事务提交，稍等再同步；漏掉的由定时同步兜底
	carpoolChangeHookDelay = 2 * time.Second
)

// CarpoolService 拼车管理：系统管理员建车/调座位/管成员；车内 manager 可调整账号、额度与车位比例；
// 所有成员可查看同车成员用量，不同拼车之间互不可见。
//
// 车位与拼车分组订阅双向绑定：在订阅页给拼车分组分配订阅即占一个车位，加车位即分配订阅，
// 撤销订阅即释放车位。每个车位按百分比分得账号周额度，写到该订阅的周限额覆盖上；
// 订阅周窗口对齐账号 7 天窗口，账号窗口重置（含使用重置卡）时成员周用量一并清零。
type CarpoolService struct {
	repo                CarpoolRepository
	adminService        AdminService
	subscriptionService *SubscriptionService
	accountUsageService *AccountUsageService
	rateLimitService    *RateLimitService
	quotaService        *OpenAIQuotaService
	userRepo            UserRepository
	// invalidateSub 直接改写订阅行后失效订阅缓存（测试可替换）
	invalidateSub func(userID, groupID int64)

	// mu 串行化同一实例内的车位同步，避免订阅回调与管理操作并发插入同一车位
	mu       sync.Mutex
	stopCh   chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewCarpoolService(
	repo CarpoolRepository,
	adminService AdminService,
	subscriptionService *SubscriptionService,
	accountUsageService *AccountUsageService,
	rateLimitService *RateLimitService,
	quotaService *OpenAIQuotaService,
	userRepo UserRepository,
) *CarpoolService {
	s := &CarpoolService{
		repo:                repo,
		adminService:        adminService,
		subscriptionService: subscriptionService,
		accountUsageService: accountUsageService,
		rateLimitService:    rateLimitService,
		quotaService:        quotaService,
		userRepo:            userRepo,
		stopCh:              make(chan struct{}),
	}
	s.invalidateSub = func(userID, groupID int64) {
		if subscriptionService == nil {
			return
		}
		if err := subscriptionService.InvalidateSubscriptionCaches(userID, groupID); err != nil {
			logger.LegacyPrintf("service.carpool", "[Carpool] invalidate subscription cache failed: user=%d group=%d err=%v", userID, groupID, err)
		}
	}
	return s
}

// Start 启动定时同步（车位、周额度预估、订阅限额与窗口）。
func (s *CarpoolService) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(carpoolSyncInterval)
		defer ticker.Stop()
		s.syncAll()
		for {
			select {
			case <-ticker.C:
				s.syncAll()
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop 停止定时同步。
func (s *CarpoolService) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

// OnSubscriptionChange 订阅变更回调：变更落在拼车分组上时同步该车车位。
func (s *CarpoolService) OnSubscriptionChange(groupID int64) {
	select {
	case <-time.After(carpoolChangeHookDelay):
	case <-s.stopCh:
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), carpoolSyncInterval)
	defer cancel()
	carpools, err := s.repo.List(ctx)
	if err != nil {
		return
	}
	for i := range carpools {
		if carpools[i].GroupID == groupID {
			if err := s.sync(ctx, &carpools[i]); err != nil {
				logger.LegacyPrintf("service.carpool", "[Carpool] sync after subscription change failed: carpool=%d err=%v", carpools[i].ID, err)
			}
		}
	}
}

func (s *CarpoolService) syncAll() {
	ctx, cancel := context.WithTimeout(context.Background(), carpoolSyncInterval)
	defer cancel()
	carpools, err := s.repo.List(ctx)
	if err != nil {
		logger.LegacyPrintf("service.carpool", "[Carpool] list carpools failed: %v", err)
		return
	}
	for i := range carpools {
		if err := s.sync(ctx, &carpools[i]); err != nil {
			logger.LegacyPrintf("service.carpool", "[Carpool] sync failed: carpool=%d err=%v", carpools[i].ID, err)
		}
	}
}

// sync 加锁执行一次完整同步：车位 ↔ 订阅、周额度预估、分组兜底限额、各订阅限额与周窗口。
func (s *CarpoolService) sync(ctx context.Context, c *Carpool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.syncLocked(ctx, c, time.Now())
}

func (s *CarpoolService) syncLocked(ctx context.Context, c *Carpool, now time.Time) error {
	if err := s.reconcileMembers(ctx, c); err != nil {
		return err
	}
	account, err := s.adminService.GetAccount(ctx, c.AccountID)
	if err != nil {
		return err
	}
	window := carpoolWindowFromAccount(account, now)
	if window != nil {
		if err := s.refreshEstimate(ctx, c, window, now); err != nil {
			return err
		}
	}
	return s.syncLimits(ctx, c, window)
}

// reconcileMembers 车位与拼车分组订阅双向对齐：有订阅没车位的补车位，车位订阅被撤销的释放车位。
func (s *CarpoolService) reconcileMembers(ctx context.Context, c *Carpool) error {
	live, err := s.repo.ListLiveGroupSubscriptions(ctx, c.GroupID)
	if err != nil {
		return err
	}
	members, err := s.repo.ListMembers(ctx, c.ID)
	if err != nil {
		return err
	}
	c.MemberCount = 0
	c.AllocatedPercent = 0
	seated := make(map[int64]struct{}, len(members))
	for _, m := range members {
		subID, ok := live[m.UserID]
		if !ok {
			if err := s.repo.RemoveMember(ctx, c.ID, m.UserID); err != nil && !errors.Is(err, ErrCarpoolMemberNotFound) {
				return err
			}
			continue
		}
		if m.SubscriptionID == nil || *m.SubscriptionID != subID {
			if err := s.repo.SetMemberSubscription(ctx, c.ID, m.UserID, subID); err != nil {
				return err
			}
		}
		seated[m.UserID] = struct{}{}
		c.MemberCount++
		c.AllocatedPercent += m.SharePercent
	}

	newcomers := make([]int64, 0)
	for userID := range live {
		if _, ok := seated[userID]; !ok {
			newcomers = append(newcomers, userID)
		}
	}
	sort.Slice(newcomers, func(i, j int) bool { return live[newcomers[i]] < live[newcomers[j]] })
	seatsBefore := c.Seats
	for _, userID := range newcomers {
		if c.MemberCount >= c.Seats {
			// 订阅页直接分配的订阅超出座位数时自动加座；新车位分到剩余比例，没有剩余则为 0，等管理员分配
			c.Seats = c.MemberCount + 1
		}
		subID := live[userID]
		share := c.DefaultSharePercent()
		err := s.repo.AddMember(ctx, &CarpoolMember{
			CarpoolID:      c.ID,
			UserID:         userID,
			SubscriptionID: &subID,
			Role:           CarpoolRoleManager,
			SharePercent:   share,
		})
		if err != nil && !errors.Is(err, ErrCarpoolMemberExists) {
			return err
		}
		c.MemberCount++
		c.AllocatedPercent += share
	}
	if c.Seats != seatsBefore {
		if err := s.repo.Update(ctx, c); err != nil {
			return err
		}
	}
	return nil
}

// refreshEstimate 用账号 7 天窗口快照刷新周额度预估。
//
// 样本只算基线之后的增量：首次跟踪时基线取当前已用百分比，排除建车前（可能未经本站）的用量；
// 跟踪中进入新窗口（自然重置或使用重置卡）时基线取窗口起点。新窗口样本不足前沿用上一次预估。
func (s *CarpoolService) refreshEstimate(ctx context.Context, c *Carpool, window *carpoolWindowSample, now time.Time) error {
	state := CarpoolEstimateState{
		EstimatedWeeklyUSD:  c.EstimatedWeeklyUSD,
		WindowResetAt:       c.WindowResetAt,
		BaselineAt:          c.BaselineAt,
		BaselineUsedPercent: c.BaselineUsedPercent,
	}
	newWindow := window.isNewWindow(c)
	if newWindow {
		resetAt := window.ResetAt
		state.WindowResetAt = &resetAt
		if c.WindowResetAt != nil || c.countCurrentWindow {
			// 跟踪中进入新窗口，或建车时声明账号只服务本站：样本从窗口起点算起
			start := resetAt.Add(-carpoolUpstreamWindow)
			state.BaselineAt = &start
			state.BaselineUsedPercent = 0
		} else {
			baseline := now
			state.BaselineAt = &baseline
			state.BaselineUsedPercent = window.UsedPercent
		}
	}
	if state.BaselineAt != nil && window.UsedPercent-state.BaselineUsedPercent >= CarpoolMinEstimatePercent {
		cost, err := s.repo.GetAccountActualCostSince(ctx, c.AccountID, *state.BaselineAt)
		if err != nil {
			return err
		}
		if estimated, ok := CarpoolEstimateWeekly(cost, window.UsedPercent-state.BaselineUsedPercent); ok {
			state.EstimatedWeeklyUSD = estimated
		}
	}
	// 没有有效预估时保留当前额度（可能是建车时给的初始额度）
	state.ApplyToQuota = c.QuotaSource == CarpoolQuotaSourceAuto && state.EstimatedWeeklyUSD > 0 &&
		carpoolQuotaChanged(c.WeeklyQuotaUSD, state.EstimatedWeeklyUSD)
	if !newWindow && !state.ApplyToQuota && state.EstimatedWeeklyUSD == c.EstimatedWeeklyUSD {
		return nil
	}
	saved, err := s.repo.SaveEstimateState(ctx, c.ID, &state, c.WindowResetAt)
	if err != nil || !saved {
		// 没写成说明其他实例已处理该窗口，重新读一次保持后续同步用最新值
		if fresh, getErr := s.repo.GetByID(ctx, c.ID); getErr == nil {
			fresh.MemberCount, fresh.AllocatedPercent = c.MemberCount, c.AllocatedPercent
			*c = *fresh
		}
		return err
	}
	c.EstimatedWeeklyUSD = state.EstimatedWeeklyUSD
	c.WindowResetAt = state.WindowResetAt
	c.BaselineAt = state.BaselineAt
	c.BaselineUsedPercent = state.BaselineUsedPercent
	if state.ApplyToQuota {
		c.WeeklyQuotaUSD = state.EstimatedWeeklyUSD
	}
	return nil
}

// syncLimits 把额度写到分组（兜底）与各车位订阅，并把订阅周窗口对齐账号窗口。
// 只改分组周限额，模型定价、倍率等其他分组设置不动；分组页手动改的周限额会被修正回来。
func (s *CarpoolService) syncLimits(ctx context.Context, c *Carpool, window *carpoolWindowSample) error {
	if err := s.syncGroupLimit(ctx, c); err != nil {
		return err
	}
	members, err := s.repo.ListMembers(ctx, c.ID)
	if err != nil {
		return err
	}
	for _, m := range members {
		if m.SubscriptionID == nil {
			continue
		}
		changed, err := s.repo.SetSubscriptionWeeklyLimit(ctx, *m.SubscriptionID, c.MemberWeeklyLimit(m.SharePercent))
		if err != nil {
			return err
		}
		if window != nil {
			aligned, err := s.repo.AlignSubscriptionWeeklyWindow(ctx, *m.SubscriptionID, window.ResetAt.Add(-carpoolUpstreamWindow))
			if err != nil {
				return err
			}
			changed = changed || aligned
		}
		if changed {
			s.invalidateSub(m.UserID, c.GroupID)
		}
	}
	return nil
}

func (s *CarpoolService) syncGroupLimit(ctx context.Context, c *Carpool) error {
	group, err := s.adminService.GetGroup(ctx, c.GroupID)
	if err != nil {
		return fmt.Errorf("get carpool group: %w", err)
	}
	desired := carpoolGroupWeeklyLimit(c)
	current := -1.0
	if group.WeeklyLimitUSD != nil {
		current = *group.WeeklyLimitUSD
	}
	if math.Abs(current-desired) < 1e-4 {
		return nil
	}
	if _, err := s.adminService.UpdateGroup(ctx, c.GroupID, &UpdateGroupInput{WeeklyLimitUSD: &desired}); err != nil {
		return fmt.Errorf("sync carpool group weekly limit: %w", err)
	}
	return nil
}

type CarpoolMemberInput struct {
	UserID       int64
	Email        string
	Role         string
	SharePercent *float64 // nil 表示按剩余比例均分
}

type CreateCarpoolInput struct {
	Name      string
	AccountID int64
	// GroupID > 0 时接管已有的 OpenAI 订阅分组（保留分组设置、API Key 与订阅），否则新建专属分组
	GroupID        int64
	WeeklyQuotaUSD *float64 // 非 nil 表示手动指定账号周额度，否则跟随自动预估
	// InitialWeeklyQuotaUSD 自动预估模式下首个预估生成前使用的账号周额度；nil 表示期间不限额
	InitialWeeklyQuotaUSD *float64
	// CountCurrentWindow 账号只服务本站时，把当前 7 天窗口已有用量计入首个预估样本
	CountCurrentWindow bool
	Seats              int
	ReserveRatio       float64 // 整车预留比例（0-1）
	ValidityDays       int
	Notes              string
	// ExclusiveAccount 为 true 时账号只保留拼车分组，避免车外流量吃掉额度
	ExclusiveAccount bool
	Members          []CarpoolMemberInput
	ActorID          int64
}

// UpdateCarpoolQuotaInput 额度参数；nil 表示不修改。车内 manager 与系统管理员共用。
type UpdateCarpoolQuotaInput struct {
	QuotaSource    *string  // auto 恢复跟随预估
	WeeklyQuotaUSD *float64 // 非 nil 表示手动指定
	ReserveRatio   *float64 // 整车预留比例（0-1）
}

// AdminUpdateCarpoolInput 仅系统管理员可改的字段 + 额度参数。
type AdminUpdateCarpoolInput struct {
	UpdateCarpoolQuotaInput
	Name         *string
	Notes        *string
	Seats        *int
	ValidityDays *int
}

// ---------- 视图 ----------

type CarpoolView struct {
	ID                  int64     `json:"id"`
	Name                string    `json:"name"`
	AccountID           int64     `json:"account_id"`
	GroupID             int64     `json:"group_id"`
	WeeklyQuotaUSD      float64   `json:"weekly_quota_usd"`
	QuotaSource         string    `json:"quota_source"`
	ReserveRatio        float64   `json:"reserve_ratio"`
	DistributableWeekly float64   `json:"distributable_weekly_usd"`
	Seats               int       `json:"seats"`
	AllocatedPercent    float64   `json:"allocated_percent"`
	ValidityDays        int       `json:"validity_days"`
	Notes               string    `json:"notes"`
	Status              string    `json:"status"`
	MemberCount         int       `json:"member_count"`
	CreatedAt           time.Time `json:"created_at"`
	MyRole              string    `json:"my_role,omitempty"`
}

type CarpoolAccountView struct {
	ID                     int64          `json:"id"`
	Name                   string         `json:"name"`
	Platform               string         `json:"platform"`
	Type                   string         `json:"type"`
	Status                 string         `json:"status"`
	Schedulable            bool           `json:"schedulable"`
	ResetCardSupported     bool           `json:"reset_card_supported"`
	ErrorMessage           string         `json:"error_message,omitempty"`
	RateLimitResetAt       *time.Time     `json:"rate_limit_reset_at,omitempty"`
	TempUnschedulableUntil *time.Time     `json:"temp_unschedulable_until,omitempty"`
	OverloadUntil          *time.Time     `json:"overload_until,omitempty"`
	LastUsedAt             *time.Time     `json:"last_used_at,omitempty"`
	FiveHour               *UsageProgress `json:"five_hour,omitempty"`
	SevenDay               *UsageProgress `json:"seven_day,omitempty"`
}

type CarpoolEstimate struct {
	EstimatedWeeklyUSD float64    `json:"estimated_weekly_usd"` // 0 表示尚未产生有效预估
	SampleCostUSD      float64    `json:"sample_cost_usd"`
	SampleUsedPercent  float64    `json:"sample_used_percent"`
	SampleStart        *time.Time `json:"sample_start,omitempty"`
	WindowResetAt      *time.Time `json:"window_reset_at,omitempty"`
	MinSamplePercent   float64    `json:"min_sample_percent"`
	AppliedWeeklyUSD   float64    `json:"applied_weekly_usd"`
}

type CarpoolMemberView struct {
	UserID             int64      `json:"user_id"`
	Email              string     `json:"email"`
	Username           string     `json:"username"`
	Role               string     `json:"role"`
	IsMe               bool       `json:"is_me"`
	JoinedAt           time.Time  `json:"joined_at"`
	SharePercent       float64    `json:"share_percent"`
	SubscriptionID     *int64     `json:"subscription_id,omitempty"`
	SubscriptionStatus string     `json:"subscription_status,omitempty"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	WeeklyUsageUSD     float64    `json:"weekly_usage_usd"`
	WeeklyLimitUSD     *float64   `json:"weekly_limit_usd"` // nil 表示尚未预估，暂不限额
	WeeklyResetsAt     *time.Time `json:"weekly_resets_at,omitempty"`
	DailyUsageUSD      float64    `json:"daily_usage_usd"`
	MonthlyUsageUSD    float64    `json:"monthly_usage_usd"`
	Requests7d         int64      `json:"requests_7d"`
	Tokens7d           int64      `json:"tokens_7d"`
	Cost7dUSD          float64    `json:"cost_7d_usd"`
	LastUsedAt         *time.Time `json:"last_used_at,omitempty"`
}

type CarpoolDetail struct {
	Carpool  CarpoolView         `json:"carpool"`
	Account  *CarpoolAccountView `json:"account"`
	Estimate CarpoolEstimate     `json:"estimate"`
	Members  []CarpoolMemberView `json:"members"`
}

// CarpoolResetCards 账号重置卡状态。
type CarpoolResetCards struct {
	Supported        bool                               `json:"supported"`
	AvailableCount   int                                `json:"available_count"`
	Credits          []OpenAIRateLimitResetCreditDetail `json:"credits,omitempty"`
	UsedPercent      float64                            `json:"used_percent"`
	ThresholdPercent float64                            `json:"threshold_percent"`
	CanUse           bool                               `json:"can_use"`
}

// CarpoolResetCardResult 使用重置卡的结果。
type CarpoolResetCardResult struct {
	Result      *OpenAIQuotaResetResult `json:"result"`
	WarningCode string                  `json:"warning_code,omitempty"`
}

func carpoolToView(c *Carpool, myRole string) CarpoolView {
	return CarpoolView{
		ID:                  c.ID,
		Name:                c.Name,
		AccountID:           c.AccountID,
		GroupID:             c.GroupID,
		WeeklyQuotaUSD:      c.WeeklyQuotaUSD,
		QuotaSource:         c.QuotaSource,
		ReserveRatio:        c.ReserveRatio,
		DistributableWeekly: c.DistributableWeeklyUSD(),
		Seats:               c.Seats,
		AllocatedPercent:    roundCarpoolPercent(c.AllocatedPercent),
		ValidityDays:        c.ValidityDays,
		Notes:               c.Notes,
		Status:              c.Status,
		MemberCount:         c.MemberCount,
		CreatedAt:           c.CreatedAt,
		MyRole:              myRole,
	}
}

func roundCarpoolPercent(v float64) float64 {
	return math.Round(v*100) / 100
}

// ---------- 系统管理员 ----------

func (s *CarpoolService) AdminList(ctx context.Context) ([]CarpoolView, error) {
	items, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CarpoolView, 0, len(items))
	for i := range items {
		if err := s.sync(ctx, &items[i]); err != nil {
			logger.LegacyPrintf("service.carpool", "[Carpool] sync on list failed: carpool=%d err=%v", items[i].ID, err)
		}
		out = append(out, carpoolToView(&items[i], ""))
	}
	return out, nil
}

func (s *CarpoolService) AdminCreate(ctx context.Context, input *CreateCarpoolInput) (*CarpoolView, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, ErrCarpoolInvalidName
	}
	if err := validateCarpoolSeats(input.Seats); err != nil {
		return nil, err
	}
	if len(input.Members) > input.Seats {
		return nil, ErrCarpoolSeatsFull
	}
	if err := validateCarpoolReserveRatio(input.ReserveRatio); err != nil {
		return nil, err
	}
	source := CarpoolQuotaSourceAuto
	weekly := 0.0
	if input.WeeklyQuotaUSD != nil {
		if *input.WeeklyQuotaUSD <= 0 {
			return nil, ErrCarpoolInvalidQuota
		}
		weekly = roundCarpoolUSD(*input.WeeklyQuotaUSD)
		source = CarpoolQuotaSourceManual
	} else if input.InitialWeeklyQuotaUSD != nil {
		if *input.InitialWeeklyQuotaUSD <= 0 {
			return nil, ErrCarpoolInvalidQuota
		}
		weekly = roundCarpoolUSD(*input.InitialWeeklyQuotaUSD)
	}
	validityDays := input.ValidityDays
	if validityDays <= 0 {
		validityDays = 30
	}

	account, err := s.adminService.GetAccount(ctx, input.AccountID)
	if err != nil {
		return nil, err
	}
	if account.Platform != domain.PlatformOpenAI {
		return nil, ErrCarpoolAccountPlatform
	}
	if existing, err := s.repo.GetByAccountID(ctx, account.ID); err == nil && existing != nil {
		return nil, ErrCarpoolAccountTaken
	} else if err != nil && !errors.Is(err, ErrCarpoolNotFound) {
		return nil, err
	}

	var adopted *Group
	if input.GroupID > 0 {
		adopted, err = s.adminService.GetGroup(ctx, input.GroupID)
		if err != nil {
			return nil, err
		}
		if adopted.Platform != domain.PlatformOpenAI || !adopted.IsSubscriptionType() || adopted.Status != domain.StatusActive {
			return nil, ErrCarpoolGroupInvalid
		}
		existing, err := s.repo.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, c := range existing {
			if c.GroupID == adopted.ID {
				return nil, ErrCarpoolGroupTaken
			}
		}
	}

	// 先解析成员，避免建完分组才发现用户不存在
	members, err := s.resolveMembers(ctx, input.Members)
	if err != nil {
		return nil, err
	}
	if err := validateCarpoolMemberShares(members); err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	actor := input.ActorID
	carpool := &Carpool{
		Name:           name,
		AccountID:      account.ID,
		WeeklyQuotaUSD: weekly,
		QuotaSource:    source,
		Seats:          input.Seats,
		ReserveRatio:   input.ReserveRatio,
		ValidityDays:   validityDays,
		Notes:          strings.TrimSpace(input.Notes),
		Status:         domain.StatusActive,
		CreatedBy:      &actor,

		countCurrentWindow: input.CountCurrentWindow,
	}
	group := adopted
	if group == nil {
		limit := carpoolGroupWeeklyLimit(carpool)
		group, err = s.adminService.CreateGroup(ctx, &CreateGroupInput{
			Name:                      "拼车-" + name,
			Description:               "拼车专属分组。周限额由拼车自动维护，模型定价等其他设置可在此调整",
			Platform:                  domain.PlatformOpenAI,
			RateMultiplier:            1.0,
			IsExclusive:               true,
			SubscriptionType:          domain.SubscriptionTypeSubscription,
			WeeklyLimitUSD:            &limit,
			LongContextPricingEnabled: true,
		})
		if err != nil {
			return nil, fmt.Errorf("create carpool group: %w", err)
		}
	}
	carpool.GroupID = group.ID

	groupIDs := []int64{group.ID}
	if !input.ExclusiveAccount {
		groupIDs = append(append([]int64{}, account.GroupIDs...), group.ID)
	}
	if _, err := s.adminService.UpdateAccount(ctx, account.ID, &UpdateAccountInput{
		GroupIDs:              &groupIDs,
		SkipMixedChannelCheck: true,
	}); err != nil {
		if adopted == nil {
			_ = s.adminService.DeleteGroup(ctx, group.ID)
		}
		return nil, fmt.Errorf("bind account to carpool group: %w", err)
	}

	if err := s.repo.Create(ctx, carpool); err != nil {
		return nil, err
	}

	for _, m := range members {
		if err := s.addMemberLocked(ctx, carpool, m, input.ActorID); err != nil {
			return nil, err
		}
	}
	// 记录建车时的窗口基线并下发各车位额度，失败不影响建车，定时同步会重试
	if err := s.syncLocked(ctx, carpool, time.Now()); err != nil {
		logger.LegacyPrintf("service.carpool", "[Carpool] initial sync failed: carpool=%d err=%v", carpool.ID, err)
	}
	view := carpoolToView(carpool, "")
	return &view, nil
}

func (s *CarpoolService) AdminUpdate(ctx context.Context, id int64, input *AdminUpdateCarpoolInput) (*CarpoolView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if input.Name != nil && strings.TrimSpace(*input.Name) != "" {
		carpool.Name = strings.TrimSpace(*input.Name)
	}
	if input.Notes != nil {
		carpool.Notes = strings.TrimSpace(*input.Notes)
	}
	if input.ValidityDays != nil && *input.ValidityDays > 0 {
		carpool.ValidityDays = *input.ValidityDays
	}
	if input.Seats != nil {
		if err := validateCarpoolSeats(*input.Seats); err != nil {
			return nil, err
		}
		if *input.Seats < carpool.MemberCount {
			return nil, ErrCarpoolSeatsBelowCount
		}
		carpool.Seats = *input.Seats
	}
	if err := applyCarpoolQuotaInput(carpool, &input.UpdateCarpoolQuotaInput); err != nil {
		return nil, err
	}
	if err := s.saveAndSyncLocked(ctx, carpool); err != nil {
		return nil, err
	}
	view := carpoolToView(carpool, "")
	return &view, nil
}

func (s *CarpoolService) AdminDelete(ctx context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	members, err := s.repo.ListMembers(ctx, id)
	if err != nil {
		return err
	}
	for _, m := range members {
		s.revokeMemberSubscription(ctx, &m)
	}
	if _, err := s.adminService.UpdateGroup(ctx, carpool.GroupID, &UpdateGroupInput{Status: domain.StatusDisabled}); err != nil {
		return fmt.Errorf("disable carpool group: %w", err)
	}
	return s.repo.SoftDelete(ctx, id)
}

// AdminAddMember 加车位：同时给该用户分配拼车分组订阅。
func (s *CarpoolService) AdminAddMember(ctx context.Context, id int64, input CarpoolMemberInput, actorID int64) error {
	resolved, err := s.resolveMembers(ctx, []CarpoolMemberInput{input})
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if err := s.reconcileMembers(ctx, carpool); err != nil {
		return err
	}
	if carpool.MemberCount >= carpool.Seats {
		return ErrCarpoolSeatsFull
	}
	if share := resolved[0].share; share != nil && carpool.AllocatedPercent+*share > 100+1e-6 {
		return ErrCarpoolInvalidShare
	}
	if err := s.addMemberLocked(ctx, carpool, resolved[0], actorID); err != nil {
		return err
	}
	return s.syncLocked(ctx, carpool, time.Now())
}

// AdminRemoveMember 释放车位：同时撤销该成员的拼车分组订阅。
func (s *CarpoolService) AdminRemoveMember(ctx context.Context, id, userID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	member, err := s.repo.GetMember(ctx, id, userID)
	if err != nil {
		return err
	}
	s.revokeMemberSubscription(ctx, member)
	return s.repo.RemoveMember(ctx, id, userID)
}

func (s *CarpoolService) AdminSetMemberRole(ctx context.Context, id, userID int64, role string) error {
	normalized, err := normalizeCarpoolRole(role)
	if err != nil {
		return err
	}
	if _, err := s.repo.GetMember(ctx, id, userID); err != nil {
		return err
	}
	return s.repo.UpdateMemberRole(ctx, id, userID, normalized)
}

// SetMemberShare 调整车位可用额度百分比；actorID=0 表示系统管理员，否则须是车内 manager。
func (s *CarpoolService) SetMemberShare(ctx context.Context, id, actorID, userID int64, sharePercent float64) (*CarpoolView, error) {
	if err := validateCarpoolShare(sharePercent); err != nil {
		return nil, err
	}
	role := ""
	if actorID != 0 {
		_, actor, err := s.loadForManager(ctx, id, actorID)
		if err != nil {
			return nil, err
		}
		role = actor.Role
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	members, err := s.repo.ListMembers(ctx, id)
	if err != nil {
		return nil, err
	}
	others, found := 0.0, false
	for _, m := range members {
		if m.UserID == userID {
			found = true
			continue
		}
		others += m.SharePercent
	}
	if !found {
		return nil, ErrCarpoolMemberNotFound
	}
	if others+sharePercent > 100+1e-6 {
		return nil, ErrCarpoolInvalidShare
	}
	if err := s.repo.UpdateMemberShare(ctx, id, userID, roundCarpoolPercent(sharePercent)); err != nil {
		return nil, err
	}
	if err := s.syncLocked(ctx, carpool, time.Now()); err != nil {
		return nil, err
	}
	view := carpoolToView(carpool, role)
	return &view, nil
}

// AdminRenew 给所有成员续期一个周期（ValidityDays 天）。
func (s *CarpoolService) AdminRenew(ctx context.Context, id, actorID int64) error {
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	members, err := s.repo.ListMembers(ctx, id)
	if err != nil {
		return err
	}
	for _, m := range members {
		if _, _, err := s.subscriptionService.AssignOrExtendSubscription(ctx, &AssignSubscriptionInput{
			UserID:       m.UserID,
			GroupID:      carpool.GroupID,
			ValidityDays: carpool.ValidityDays,
			AssignedBy:   actorID,
			Notes:        fmt.Sprintf("carpool #%d renew", carpool.ID),
		}); err != nil {
			return fmt.Errorf("renew subscription for user %d: %w", m.UserID, err)
		}
	}
	return s.sync(ctx, carpool)
}

func (s *CarpoolService) AdminGetDetail(ctx context.Context, id int64) (*CarpoolDetail, error) {
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.sync(ctx, carpool); err != nil {
		logger.LegacyPrintf("service.carpool", "[Carpool] sync on detail failed: carpool=%d err=%v", id, err)
	}
	return s.buildDetail(ctx, carpool, 0, "", false)
}

// ---------- 车内成员 ----------

// ListMine 列出用户所在的拼车；在订阅页刚分配的拼车分组订阅也会在这里同步成车位。
func (s *CarpoolService) ListMine(ctx context.Context, userID int64) ([]CarpoolView, error) {
	items, err := s.repo.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]CarpoolView, 0, len(items))
	for i := range items {
		if err := s.sync(ctx, &items[i]); err != nil {
			logger.LegacyPrintf("service.carpool", "[Carpool] sync on list failed: carpool=%d err=%v", items[i].ID, err)
		}
		m, err := s.repo.GetMember(ctx, items[i].ID, userID)
		if err != nil {
			continue // 订阅已撤销，车位刚被释放
		}
		out = append(out, carpoolToView(&items[i], m.Role))
	}
	return out, nil
}

func (s *CarpoolService) GetDetail(ctx context.Context, id, userID int64) (*CarpoolDetail, error) {
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := s.sync(ctx, carpool); err != nil {
		logger.LegacyPrintf("service.carpool", "[Carpool] sync on detail failed: carpool=%d err=%v", id, err)
	}
	member, err := s.repo.GetMember(ctx, id, userID)
	if err != nil {
		if errors.Is(err, ErrCarpoolMemberNotFound) {
			return nil, ErrCarpoolNotFound
		}
		return nil, err
	}
	return s.buildDetail(ctx, carpool, userID, member.Role, true)
}

func (s *CarpoolService) ManagerUpdateQuota(ctx context.Context, id, userID int64, input *UpdateCarpoolQuotaInput) (*CarpoolView, error) {
	_, member, err := s.loadForManager(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := applyCarpoolQuotaInput(carpool, input); err != nil {
		return nil, err
	}
	if err := s.saveAndSyncLocked(ctx, carpool); err != nil {
		return nil, err
	}
	view := carpoolToView(carpool, member.Role)
	return &view, nil
}

const (
	CarpoolAccountActionSetSchedulable = "set_schedulable"
	CarpoolAccountActionClearError     = "clear_error"
	CarpoolAccountActionClearRateLimit = "clear_rate_limit"
	CarpoolAccountActionRefreshUsage   = "refresh_usage"
)

// AccountAction 车内 manager 对本车账号的运维操作；userID=0 表示系统管理员调用。
func (s *CarpoolService) AccountAction(ctx context.Context, id, userID int64, action string, schedulable *bool) error {
	carpool, err := s.loadCarpoolForManager(ctx, id, userID)
	if err != nil {
		return err
	}
	switch action {
	case CarpoolAccountActionSetSchedulable:
		if schedulable == nil {
			return ErrCarpoolInvalidAction
		}
		_, err = s.adminService.SetAccountSchedulable(ctx, carpool.AccountID, *schedulable)
	case CarpoolAccountActionClearError:
		_, err = s.adminService.ClearAccountError(ctx, carpool.AccountID)
	case CarpoolAccountActionClearRateLimit:
		err = s.rateLimitService.ClearRateLimit(ctx, carpool.AccountID)
	case CarpoolAccountActionRefreshUsage:
		if _, err = s.accountUsageService.GetUsage(ctx, carpool.AccountID, true); err == nil {
			err = s.sync(ctx, carpool)
		}
	default:
		return ErrCarpoolInvalidAction
	}
	return err
}

// GetResetCards 查看账号重置卡；所有成员可看，manager 在整车用量达标后可用。userID=0 表示系统管理员。
func (s *CarpoolService) GetResetCards(ctx context.Context, id, userID int64) (*CarpoolResetCards, error) {
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	canManage := userID == 0
	if userID != 0 {
		member, err := s.repo.GetMember(ctx, id, userID)
		if err != nil {
			if errors.Is(err, ErrCarpoolMemberNotFound) {
				return nil, ErrCarpoolNotFound
			}
			return nil, err
		}
		canManage = member.Role == CarpoolRoleManager
	}
	cards, _, err := s.queryResetCards(ctx, carpool)
	if err != nil || !cards.Supported {
		return cards, err
	}
	cards.CanUse = canManage && cards.AvailableCount > 0 && cards.UsedPercent >= CarpoolResetCardThresholdPercent
	return cards, nil
}

// UseResetCard 使用一张账号重置卡：仅 manager，且整车（账号 7 天窗口）已用满 80%。
// 用后立即同步，成员周用量随账号窗口重置清零。
func (s *CarpoolService) UseResetCard(ctx context.Context, id, userID int64) (*CarpoolResetCardResult, error) {
	carpool, err := s.loadCarpoolForManager(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	cards, account, err := s.queryResetCards(ctx, carpool)
	if err != nil {
		return nil, err
	}
	if !cards.Supported {
		return nil, ErrCarpoolResetUnsupported
	}
	if cards.UsedPercent < CarpoolResetCardThresholdPercent {
		return nil, ErrCarpoolResetNotAllowed
	}
	result, err := s.quotaService.ResetCredit(ctx, account.ID)
	if err != nil {
		return nil, err
	}
	post := RunOpenAIQuotaResetPostProcess(ctx, account.ID, s.quotaService, s.rateLimitService, s.adminService.GetAccount)
	if err := s.sync(ctx, carpool); err != nil {
		logger.LegacyPrintf("service.carpool", "[Carpool] sync after reset card failed: carpool=%d err=%v", carpool.ID, err)
	}
	return &CarpoolResetCardResult{Result: result, WarningCode: post.WarningCode}, nil
}

// queryResetCards 实时查询上游重置卡与 7 天窗口用量；整车用量取上游与本地快照中较大者。
func (s *CarpoolService) queryResetCards(ctx context.Context, carpool *Carpool) (*CarpoolResetCards, *Account, error) {
	cards := &CarpoolResetCards{ThresholdPercent: CarpoolResetCardThresholdPercent}
	account, err := s.adminService.GetAccount(ctx, carpool.AccountID)
	if err != nil {
		return nil, nil, err
	}
	if !carpoolSupportsResetCard(account) || s.quotaService == nil {
		return cards, account, nil
	}
	cards.Supported = true
	if window := carpoolWindowFromAccount(account, time.Now()); window != nil {
		cards.UsedPercent = window.UsedPercent
	}
	usage, err := s.quotaService.QueryUsage(ctx, account.ID)
	if err != nil {
		return nil, nil, err
	}
	if usage.RateLimitResetCredits != nil {
		cards.AvailableCount = usage.RateLimitResetCredits.AvailableCount
		cards.Credits = usage.RateLimitResetCredits.Credits
	}
	if used, ok := openAIWeeklyUsedPercent(usage.RateLimit); ok && used > cards.UsedPercent {
		cards.UsedPercent = used
	}
	return cards, account, nil
}

func carpoolSupportsResetCard(account *Account) bool {
	return account != nil && account.Platform == domain.PlatformOpenAI && account.IsOAuth()
}

// openAIWeeklyUsedPercent 从上游限额信封里取 7 天窗口已用百分比。
func openAIWeeklyUsedPercent(limit *OpenAIRateLimit) (float64, bool) {
	if limit == nil {
		return 0, false
	}
	for _, w := range []*OpenAIRateLimitWindow{limit.SecondaryWindow, limit.PrimaryWindow} {
		if w != nil && w.LimitWindowSeconds >= int64((6*24*time.Hour).Seconds()) {
			return w.UsedPercent, true
		}
	}
	if limit.SecondaryWindow != nil {
		return limit.SecondaryWindow.UsedPercent, true
	}
	return 0, false
}

// ---------- 内部 ----------

type resolvedCarpoolMember struct {
	user  *User
	role  string
	share *float64
}

func (s *CarpoolService) resolveMembers(ctx context.Context, inputs []CarpoolMemberInput) ([]resolvedCarpoolMember, error) {
	out := make([]resolvedCarpoolMember, 0, len(inputs))
	seen := make(map[int64]struct{}, len(inputs))
	for _, in := range inputs {
		role, err := normalizeCarpoolRole(in.Role)
		if err != nil {
			return nil, err
		}
		if in.SharePercent != nil {
			if err := validateCarpoolShare(*in.SharePercent); err != nil {
				return nil, err
			}
		}
		var user *User
		if in.UserID > 0 {
			user, err = s.userRepo.GetByID(ctx, in.UserID)
		} else if email := strings.TrimSpace(in.Email); email != "" {
			user, err = s.userRepo.GetByEmail(ctx, email)
		} else {
			return nil, ErrUserNotFound
		}
		if err != nil {
			return nil, err
		}
		if _, dup := seen[user.ID]; dup {
			return nil, ErrCarpoolMemberExists
		}
		seen[user.ID] = struct{}{}
		out = append(out, resolvedCarpoolMember{user: user, role: role, share: in.SharePercent})
	}
	return out, nil
}

func validateCarpoolMemberShares(members []resolvedCarpoolMember) error {
	total := 0.0
	for _, m := range members {
		if m.share != nil {
			total += *m.share
		}
	}
	if total > 100+1e-6 {
		return ErrCarpoolInvalidShare
	}
	return nil
}

// addMemberLocked 加车位并分配订阅；订阅回调可能先一步补了车位，此时以本次指定的角色与比例为准。
func (s *CarpoolService) addMemberLocked(ctx context.Context, carpool *Carpool, m resolvedCarpoolMember, actorID int64) error {
	if _, err := s.repo.GetMember(ctx, carpool.ID, m.user.ID); err == nil {
		return ErrCarpoolMemberExists
	} else if !errors.Is(err, ErrCarpoolMemberNotFound) {
		return err
	}
	share := carpool.DefaultSharePercent()
	if m.share != nil {
		share = *m.share
	}
	sub, _, err := s.subscriptionService.AssignOrExtendSubscription(ctx, &AssignSubscriptionInput{
		UserID:       m.user.ID,
		GroupID:      carpool.GroupID,
		ValidityDays: carpool.ValidityDays,
		AssignedBy:   actorID,
		Notes:        fmt.Sprintf("carpool #%d", carpool.ID),
	})
	if err != nil {
		return fmt.Errorf("assign carpool subscription to user %d: %w", m.user.ID, err)
	}
	err = s.repo.AddMember(ctx, &CarpoolMember{
		CarpoolID:      carpool.ID,
		UserID:         m.user.ID,
		SubscriptionID: &sub.ID,
		Role:           m.role,
		SharePercent:   share,
	})
	if errors.Is(err, ErrCarpoolMemberExists) {
		if err := s.repo.UpdateMemberRole(ctx, carpool.ID, m.user.ID, m.role); err != nil {
			return err
		}
		err = s.repo.UpdateMemberShare(ctx, carpool.ID, m.user.ID, share)
	}
	if err != nil {
		return err
	}
	carpool.MemberCount++
	carpool.AllocatedPercent += share
	return nil
}

func (s *CarpoolService) revokeMemberSubscription(ctx context.Context, m *CarpoolMember) {
	if m.SubscriptionID == nil {
		return
	}
	_ = s.subscriptionService.RevokeSubscription(ctx, *m.SubscriptionID)
}

// loadForMember 校验成员身份；非成员统一返回 NotFound，不暴露拼车是否存在。
func (s *CarpoolService) loadForMember(ctx context.Context, id, userID int64) (*Carpool, *CarpoolMember, error) {
	member, err := s.repo.GetMember(ctx, id, userID)
	if err != nil {
		if errors.Is(err, ErrCarpoolMemberNotFound) {
			return nil, nil, ErrCarpoolNotFound
		}
		return nil, nil, err
	}
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return carpool, member, nil
}

func (s *CarpoolService) loadForManager(ctx context.Context, id, userID int64) (*Carpool, *CarpoolMember, error) {
	carpool, member, err := s.loadForMember(ctx, id, userID)
	if err != nil {
		return nil, nil, err
	}
	if member.Role != CarpoolRoleManager {
		return nil, member, ErrCarpoolForbidden
	}
	return carpool, member, nil
}

// loadCarpoolForManager userID=0 表示系统管理员，否则须是车内 manager。
func (s *CarpoolService) loadCarpoolForManager(ctx context.Context, id, userID int64) (*Carpool, error) {
	if userID == 0 {
		return s.repo.GetByID(ctx, id)
	}
	carpool, _, err := s.loadForManager(ctx, id, userID)
	return carpool, err
}

func applyCarpoolQuotaInput(c *Carpool, in *UpdateCarpoolQuotaInput) error {
	if in == nil {
		return nil
	}
	if in.ReserveRatio != nil {
		if err := validateCarpoolReserveRatio(*in.ReserveRatio); err != nil {
			return err
		}
		c.ReserveRatio = *in.ReserveRatio
	}
	switch {
	case in.QuotaSource != nil && *in.QuotaSource == CarpoolQuotaSourceAuto:
		c.QuotaSource = CarpoolQuotaSourceAuto
		c.WeeklyQuotaUSD = c.EstimatedWeeklyUSD
	case in.WeeklyQuotaUSD != nil:
		if *in.WeeklyQuotaUSD <= 0 {
			return ErrCarpoolInvalidQuota
		}
		c.WeeklyQuotaUSD = roundCarpoolUSD(*in.WeeklyQuotaUSD)
		c.QuotaSource = CarpoolQuotaSourceManual
	}
	return nil
}

// saveAndSyncLocked 持久化拼车并重新下发额度（调用方已持锁）。
func (s *CarpoolService) saveAndSyncLocked(ctx context.Context, c *Carpool) error {
	if err := s.repo.Update(ctx, c); err != nil {
		return err
	}
	return s.syncLocked(ctx, c, time.Now())
}

func (s *CarpoolService) buildEstimate(ctx context.Context, c *Carpool, window *carpoolWindowSample) CarpoolEstimate {
	est := CarpoolEstimate{
		EstimatedWeeklyUSD: c.EstimatedWeeklyUSD,
		WindowResetAt:      c.WindowResetAt,
		SampleStart:        c.BaselineAt,
		MinSamplePercent:   CarpoolMinEstimatePercent,
		AppliedWeeklyUSD:   c.WeeklyQuotaUSD,
	}
	if window == nil || c.BaselineAt == nil || window.isNewWindow(c) {
		return est
	}
	est.SampleUsedPercent = math.Max(0, window.UsedPercent-c.BaselineUsedPercent)
	if cost, err := s.repo.GetAccountActualCostSince(ctx, c.AccountID, *c.BaselineAt); err == nil {
		est.SampleCostUSD = roundCarpoolUSD(cost)
	}
	return est
}

func (s *CarpoolService) buildDetail(ctx context.Context, c *Carpool, viewerID int64, myRole string, maskEmail bool) (*CarpoolDetail, error) {
	now := time.Now()
	detail := &CarpoolDetail{Carpool: carpoolToView(c, myRole)}

	account, err := s.adminService.GetAccount(ctx, c.AccountID)
	if err == nil && account != nil {
		detail.Account = &CarpoolAccountView{
			ID:                     account.ID,
			Name:                   account.Name,
			Platform:               account.Platform,
			Type:                   account.Type,
			Status:                 account.Status,
			Schedulable:            account.Schedulable,
			ResetCardSupported:     carpoolSupportsResetCard(account) && s.quotaService != nil,
			ErrorMessage:           account.ErrorMessage,
			RateLimitResetAt:       account.RateLimitResetAt,
			TempUnschedulableUntil: account.TempUnschedulableUntil,
			OverloadUntil:          account.OverloadUntil,
			LastUsedAt:             account.LastUsedAt,
			FiveHour:               buildCodexUsageProgressFromExtra(account.Extra, "5h", now),
			SevenDay:               buildCodexUsageProgressFromExtra(account.Extra, "7d", now),
		}
	}
	window := carpoolWindowFromAccount(account, now)
	detail.Estimate = s.buildEstimate(ctx, c, window)

	members, err := s.repo.ListMembers(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	usage, err := s.repo.GetGroupUsageByUser(ctx, c.GroupID, now.Add(-carpoolUpstreamWindow))
	if err != nil {
		return nil, err
	}
	detail.Members = make([]CarpoolMemberView, 0, len(members))
	for _, m := range members {
		email := m.Email
		if maskEmail && m.UserID != viewerID {
			email = maskCarpoolEmail(email)
		}
		view := CarpoolMemberView{
			UserID:         m.UserID,
			Email:          email,
			Username:       m.Username,
			Role:           m.Role,
			IsMe:           viewerID != 0 && m.UserID == viewerID,
			JoinedAt:       m.CreatedAt,
			SharePercent:   m.SharePercent,
			SubscriptionID: m.SubscriptionID,
			WeeklyLimitUSD: c.MemberWeeklyLimit(m.SharePercent),
		}
		if m.SubscriptionID != nil && s.subscriptionService != nil {
			if sub, err := s.subscriptionService.GetByID(ctx, *m.SubscriptionID); err == nil && sub != nil {
				view.SubscriptionStatus = sub.Status
				expires := sub.ExpiresAt
				view.ExpiresAt = &expires
				view.WeeklyUsageUSD = sub.WeeklyUsageUSD
				view.WeeklyResetsAt = sub.WeeklyResetTime()
				view.DailyUsageUSD = sub.DailyUsageUSD
				view.MonthlyUsageUSD = sub.MonthlyUsageUSD
			}
		}
		// 成员周窗口对齐账号窗口，有上游快照时以它的重置时间为准
		if window != nil {
			resetAt := window.ResetAt
			view.WeeklyResetsAt = &resetAt
		}
		if u := usage[m.UserID]; u != nil {
			view.Requests7d = u.Requests
			view.Tokens7d = u.Tokens
			view.Cost7dUSD = roundCarpoolUSD(u.ActualCost)
			view.LastUsedAt = u.LastUsedAt
		}
		detail.Members = append(detail.Members, view)
	}
	return detail, nil
}

func maskCarpoolEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return email
	}
	local := email[:at]
	keep := 2
	if len(local) <= keep {
		keep = 1
	}
	return local[:keep] + "***" + email[at:]
}
