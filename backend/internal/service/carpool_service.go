package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const carpoolEstimateInterval = 5 * time.Minute

// CarpoolService 拼车管理：系统管理员建车/调座位/管成员；车内 manager 可调整账号与额度；
// 所有成员可查看同车成员用量，不同拼车之间互不可见。周额度由后台任务跟随账号 7 天窗口自动预估。
type CarpoolService struct {
	repo                CarpoolRepository
	adminService        AdminService
	subscriptionService *SubscriptionService
	accountUsageService *AccountUsageService
	rateLimitService    *RateLimitService
	userRepo            UserRepository
	// resetMemberWeekly 上游进入新窗口时清零成员周用量，并把成员周窗口对齐到当前时刻
	resetMemberWeekly func(ctx context.Context, subscriptionID int64) error

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
	userRepo UserRepository,
) *CarpoolService {
	return &CarpoolService{
		repo:                repo,
		adminService:        adminService,
		subscriptionService: subscriptionService,
		accountUsageService: accountUsageService,
		rateLimitService:    rateLimitService,
		userRepo:            userRepo,
		resetMemberWeekly: func(ctx context.Context, subscriptionID int64) error {
			_, err := subscriptionService.AdminResetQuota(ctx, subscriptionID, false, true, false)
			return err
		},
		stopCh: make(chan struct{}),
	}
}

// Start 启动周额度预估任务。
func (s *CarpoolService) Start() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(carpoolEstimateInterval)
		defer ticker.Stop()
		s.refreshAllEstimates()
		for {
			select {
			case <-ticker.C:
				s.refreshAllEstimates()
			case <-s.stopCh:
				return
			}
		}
	}()
}

// Stop 停止周额度预估任务。
func (s *CarpoolService) Stop() {
	s.stopOnce.Do(func() { close(s.stopCh) })
	s.wg.Wait()
}

func (s *CarpoolService) refreshAllEstimates() {
	ctx, cancel := context.WithTimeout(context.Background(), carpoolEstimateInterval)
	defer cancel()
	carpools, err := s.repo.List(ctx)
	if err != nil {
		logger.LegacyPrintf("service.carpool", "[Carpool] list carpools failed: %v", err)
		return
	}
	now := time.Now()
	for i := range carpools {
		if err := s.refreshEstimate(ctx, &carpools[i], now); err != nil {
			logger.LegacyPrintf("service.carpool", "[Carpool] refresh estimate failed: carpool=%d err=%v", carpools[i].ID, err)
		}
	}
}

// refreshEstimate 用账号 7 天窗口快照刷新预估，并把每人额度同步到分组周限额。
//
// 样本只算基线之后的增量：首次跟踪时基线取当前已用百分比，排除建车前（可能未经本站）的用量；
// 跟踪中进入新窗口时基线取窗口起点。新窗口样本不足前沿用上一次预估。
func (s *CarpoolService) refreshEstimate(ctx context.Context, c *Carpool, now time.Time) error {
	account, err := s.adminService.GetAccount(ctx, c.AccountID)
	if err != nil {
		return err
	}
	if window := carpoolWindowFromAccount(account, now); window != nil {
		state := CarpoolEstimateState{
			EstimatedWeeklyUSD:  c.EstimatedWeeklyUSD,
			WindowResetAt:       c.WindowResetAt,
			BaselineAt:          c.BaselineAt,
			BaselineUsedPercent: c.BaselineUsedPercent,
		}
		newWindow := window.isNewWindow(c)
		tracked := c.WindowResetAt != nil
		if newWindow {
			resetAt := window.ResetAt
			state.WindowResetAt = &resetAt
			if tracked {
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
		state.ApplyToQuota = c.QuotaSource == CarpoolQuotaSourceAuto && carpoolQuotaChanged(c.WeeklyQuotaUSD, state.EstimatedWeeklyUSD)
		if newWindow || state.ApplyToQuota || state.EstimatedWeeklyUSD != c.EstimatedWeeklyUSD {
			saved, err := s.repo.SaveEstimateState(ctx, c.ID, &state, c.WindowResetAt)
			if err != nil {
				return err
			}
			if !saved {
				return nil // 其他实例已处理该窗口
			}
			if newWindow && tracked {
				s.alignMemberWeeklyWindows(ctx, c.ID)
			}
			c.EstimatedWeeklyUSD = state.EstimatedWeeklyUSD
			c.WindowResetAt = state.WindowResetAt
			c.BaselineAt = state.BaselineAt
			c.BaselineUsedPercent = state.BaselineUsedPercent
			if state.ApplyToQuota {
				c.WeeklyQuotaUSD = state.EstimatedWeeklyUSD
			}
		}
	}
	return s.syncGroupLimit(ctx, c)
}

// alignMemberWeeklyWindows 上游窗口重置后清零成员周用量，让成员周窗口与账号同步刷新。
func (s *CarpoolService) alignMemberWeeklyWindows(ctx context.Context, carpoolID int64) {
	members, err := s.repo.ListMembers(ctx, carpoolID)
	if err != nil {
		logger.LegacyPrintf("service.carpool", "[Carpool] list members failed: carpool=%d err=%v", carpoolID, err)
		return
	}
	for _, m := range members {
		if m.SubscriptionID == nil {
			continue
		}
		if err := s.resetMemberWeekly(ctx, *m.SubscriptionID); err != nil {
			logger.LegacyPrintf("service.carpool", "[Carpool] align weekly window failed: carpool=%d subscription=%d err=%v", carpoolID, *m.SubscriptionID, err)
		}
	}
}

// syncGroupLimit 只改分组周限额，其他分组配置（模型定价、倍率等）不动。
// 周限额以拼车为准：分组页手动改过的值也会在这里被修正。
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
	UserID int64
	Email  string
	Role   string
}

type CreateCarpoolInput struct {
	Name           string
	AccountID      int64
	WeeklyQuotaUSD *float64 // 非 nil 表示手动指定账号周额度，否则跟随自动预估
	Seats          int
	ReserveRatio   float64
	ValidityDays   int
	Notes          string
	// ExclusiveAccount 为 true 时账号只保留拼车分组，避免车外流量吃掉额度
	ExclusiveAccount bool
	Members          []CarpoolMemberInput
	ActorID          int64
}

// UpdateCarpoolQuotaInput 额度参数；nil 表示不修改。车内 manager 与系统管理员共用。
type UpdateCarpoolQuotaInput struct {
	QuotaSource    *string  // auto 恢复跟随预估
	WeeklyQuotaUSD *float64 // 非 nil 表示手动指定
	ReserveRatio   *float64
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
	ID               int64     `json:"id"`
	Name             string    `json:"name"`
	AccountID        int64     `json:"account_id"`
	GroupID          int64     `json:"group_id"`
	WeeklyQuotaUSD   float64   `json:"weekly_quota_usd"`
	QuotaSource      string    `json:"quota_source"`
	Seats            int       `json:"seats"`
	ReserveRatio     float64   `json:"reserve_ratio"`
	PerSeatWeeklyUSD float64   `json:"per_seat_weekly_usd"`
	ValidityDays     int       `json:"validity_days"`
	Notes            string    `json:"notes"`
	Status           string    `json:"status"`
	MemberCount      int       `json:"member_count"`
	CreatedAt        time.Time `json:"created_at"`
	MyRole           string    `json:"my_role,omitempty"`
}

type CarpoolAccountView struct {
	ID                     int64          `json:"id"`
	Name                   string         `json:"name"`
	Platform               string         `json:"platform"`
	Type                   string         `json:"type"`
	Status                 string         `json:"status"`
	Schedulable            bool           `json:"schedulable"`
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
	PerSeatWeeklyUSD   float64    `json:"per_seat_weekly_usd"`
}

type CarpoolMemberView struct {
	UserID             int64      `json:"user_id"`
	Email              string     `json:"email"`
	Username           string     `json:"username"`
	Role               string     `json:"role"`
	IsMe               bool       `json:"is_me"`
	JoinedAt           time.Time  `json:"joined_at"`
	SubscriptionID     *int64     `json:"subscription_id,omitempty"`
	SubscriptionStatus string     `json:"subscription_status,omitempty"`
	ExpiresAt          *time.Time `json:"expires_at,omitempty"`
	WeeklyUsageUSD     float64    `json:"weekly_usage_usd"`
	WeeklyLimitUSD     float64    `json:"weekly_limit_usd"` // 0 表示尚未预估，暂不限额
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

func carpoolToView(c *Carpool, myRole string) CarpoolView {
	return CarpoolView{
		ID:               c.ID,
		Name:             c.Name,
		AccountID:        c.AccountID,
		GroupID:          c.GroupID,
		WeeklyQuotaUSD:   c.WeeklyQuotaUSD,
		QuotaSource:      c.QuotaSource,
		Seats:            c.Seats,
		ReserveRatio:     c.ReserveRatio,
		PerSeatWeeklyUSD: c.PerSeatWeeklyUSD(),
		ValidityDays:     c.ValidityDays,
		Notes:            c.Notes,
		Status:           c.Status,
		MemberCount:      c.MemberCount,
		CreatedAt:        c.CreatedAt,
		MyRole:           myRole,
	}
}

// ---------- 系统管理员 ----------

func (s *CarpoolService) AdminList(ctx context.Context) ([]CarpoolView, error) {
	items, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]CarpoolView, 0, len(items))
	for i := range items {
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

	// 先解析成员，避免建完分组才发现用户不存在
	members, err := s.resolveMembers(ctx, input.Members)
	if err != nil {
		return nil, err
	}

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
	}
	limit := carpoolGroupWeeklyLimit(carpool)
	group, err := s.adminService.CreateGroup(ctx, &CreateGroupInput{
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
	carpool.GroupID = group.ID

	groupIDs := []int64{group.ID}
	if !input.ExclusiveAccount {
		groupIDs = append(append([]int64{}, account.GroupIDs...), group.ID)
	}
	if _, err := s.adminService.UpdateAccount(ctx, account.ID, &UpdateAccountInput{
		GroupIDs:              &groupIDs,
		SkipMixedChannelCheck: true,
	}); err != nil {
		_ = s.adminService.DeleteGroup(ctx, group.ID)
		return nil, fmt.Errorf("bind account to carpool group: %w", err)
	}

	if err := s.repo.Create(ctx, carpool); err != nil {
		return nil, err
	}

	for _, m := range members {
		if err := s.addMember(ctx, carpool, m.user, m.role, input.ActorID); err != nil {
			return nil, err
		}
	}
	carpool.MemberCount = len(members)
	// 记录建车时的窗口基线，失败不影响建车，后台任务会重试
	if err := s.refreshEstimate(ctx, carpool, time.Now()); err != nil {
		logger.LegacyPrintf("service.carpool", "[Carpool] initial estimate failed: carpool=%d err=%v", carpool.ID, err)
	}
	view := carpoolToView(carpool, "")
	return &view, nil
}

func (s *CarpoolService) AdminUpdate(ctx context.Context, id int64, input *AdminUpdateCarpoolInput) (*CarpoolView, error) {
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
	if err := s.saveAndSyncLimit(ctx, carpool); err != nil {
		return nil, err
	}
	view := carpoolToView(carpool, "")
	return &view, nil
}

func (s *CarpoolService) AdminDelete(ctx context.Context, id int64) error {
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

func (s *CarpoolService) AdminAddMember(ctx context.Context, id int64, input CarpoolMemberInput, actorID int64) error {
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return err
	}
	if carpool.MemberCount >= carpool.Seats {
		return ErrCarpoolSeatsFull
	}
	resolved, err := s.resolveMembers(ctx, []CarpoolMemberInput{input})
	if err != nil {
		return err
	}
	return s.addMember(ctx, carpool, resolved[0].user, resolved[0].role, actorID)
}

func (s *CarpoolService) AdminRemoveMember(ctx context.Context, id, userID int64) error {
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
		sub, _, err := s.subscriptionService.AssignOrExtendSubscription(ctx, &AssignSubscriptionInput{
			UserID:       m.UserID,
			GroupID:      carpool.GroupID,
			ValidityDays: carpool.ValidityDays,
			AssignedBy:   actorID,
			Notes:        fmt.Sprintf("carpool #%d renew", carpool.ID),
		})
		if err != nil {
			return fmt.Errorf("renew subscription for user %d: %w", m.UserID, err)
		}
		if m.SubscriptionID == nil || *m.SubscriptionID != sub.ID {
			if err := s.repo.SetMemberSubscription(ctx, carpool.ID, m.UserID, sub.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *CarpoolService) AdminGetDetail(ctx context.Context, id int64) (*CarpoolDetail, error) {
	carpool, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.buildDetail(ctx, carpool, 0, "", false)
}

// ---------- 车内成员 ----------

func (s *CarpoolService) ListMine(ctx context.Context, userID int64) ([]CarpoolView, error) {
	items, err := s.repo.ListByUser(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]CarpoolView, 0, len(items))
	for i := range items {
		role := ""
		if m, err := s.repo.GetMember(ctx, items[i].ID, userID); err == nil {
			role = m.Role
		}
		out = append(out, carpoolToView(&items[i], role))
	}
	return out, nil
}

func (s *CarpoolService) GetDetail(ctx context.Context, id, userID int64) (*CarpoolDetail, error) {
	carpool, member, err := s.loadForMember(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	return s.buildDetail(ctx, carpool, userID, member.Role, true)
}

func (s *CarpoolService) ManagerUpdateQuota(ctx context.Context, id, userID int64, input *UpdateCarpoolQuotaInput) (*CarpoolView, error) {
	carpool, member, err := s.loadForManager(ctx, id, userID)
	if err != nil {
		return nil, err
	}
	if err := applyCarpoolQuotaInput(carpool, input); err != nil {
		return nil, err
	}
	if err := s.saveAndSyncLimit(ctx, carpool); err != nil {
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
	var (
		carpool *Carpool
		err     error
	)
	if userID == 0 {
		carpool, err = s.repo.GetByID(ctx, id)
	} else {
		carpool, _, err = s.loadForManager(ctx, id, userID)
	}
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
			err = s.refreshEstimate(ctx, carpool, time.Now())
		}
	default:
		return ErrCarpoolInvalidAction
	}
	return err
}

// ---------- 内部 ----------

type resolvedCarpoolMember struct {
	user *User
	role string
}

func (s *CarpoolService) resolveMembers(ctx context.Context, inputs []CarpoolMemberInput) ([]resolvedCarpoolMember, error) {
	out := make([]resolvedCarpoolMember, 0, len(inputs))
	seen := make(map[int64]struct{}, len(inputs))
	for _, in := range inputs {
		role, err := normalizeCarpoolRole(in.Role)
		if err != nil {
			return nil, err
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
		out = append(out, resolvedCarpoolMember{user: user, role: role})
	}
	return out, nil
}

func (s *CarpoolService) addMember(ctx context.Context, carpool *Carpool, user *User, role string, actorID int64) error {
	if _, err := s.repo.GetMember(ctx, carpool.ID, user.ID); err == nil {
		return ErrCarpoolMemberExists
	} else if !errors.Is(err, ErrCarpoolMemberNotFound) {
		return err
	}
	sub, _, err := s.subscriptionService.AssignOrExtendSubscription(ctx, &AssignSubscriptionInput{
		UserID:       user.ID,
		GroupID:      carpool.GroupID,
		ValidityDays: carpool.ValidityDays,
		AssignedBy:   actorID,
		Notes:        fmt.Sprintf("carpool #%d", carpool.ID),
	})
	if err != nil {
		return fmt.Errorf("assign carpool subscription to user %d: %w", user.ID, err)
	}
	return s.repo.AddMember(ctx, &CarpoolMember{
		CarpoolID:      carpool.ID,
		UserID:         user.ID,
		SubscriptionID: &sub.ID,
		Role:           role,
	})
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

// saveAndSyncLimit 持久化拼车并把每人周额度同步到分组周限额。
func (s *CarpoolService) saveAndSyncLimit(ctx context.Context, c *Carpool) error {
	if err := s.repo.Update(ctx, c); err != nil {
		return err
	}
	return s.syncGroupLimit(ctx, c)
}

func (s *CarpoolService) buildEstimate(ctx context.Context, c *Carpool, window *carpoolWindowSample) CarpoolEstimate {
	est := CarpoolEstimate{
		EstimatedWeeklyUSD: c.EstimatedWeeklyUSD,
		WindowResetAt:      c.WindowResetAt,
		SampleStart:        c.BaselineAt,
		MinSamplePercent:   CarpoolMinEstimatePercent,
		AppliedWeeklyUSD:   c.WeeklyQuotaUSD,
		PerSeatWeeklyUSD:   c.PerSeatWeeklyUSD(),
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
	perSeat := c.PerSeatWeeklyUSD()
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
			SubscriptionID: m.SubscriptionID,
			WeeklyLimitUSD: perSeat,
		}
		if m.SubscriptionID != nil {
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
		// 成员周用量随账号窗口一起清零，有上游快照时以它的重置时间为准
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
