package service

import (
	"context"
	"math"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// 拼车：一个 OpenAI 订阅账号绑定一个专属订阅分组，分组周限额 = 每人周额度。
// 拼车只维护分组的周限额，模型定价、倍率等其余分组配置由管理员在分组页调整。
const (
	CarpoolRoleManager = "manager" // 账号级管理员：可调整账号状态与额度
	CarpoolRoleMember  = "member"  // 只读成员

	CarpoolQuotaSourceAuto   = "auto"
	CarpoolQuotaSourceManual = "manual"

	CarpoolMinSeats = 1
	CarpoolMaxSeats = 10

	// 上游已用百分比精度有限，窗口上涨低于该值时样本太小，不生成预估
	CarpoolMinEstimatePercent = 5.0

	carpoolUpstreamWindow = 7 * 24 * time.Hour
	// 上游重置时间由「当前时间 + 剩余秒数」算出，存在抖动；偏移超过该值才视为进入新窗口
	carpoolWindowResetTolerance = time.Hour
	// 预估变化小于该比例时不改额度，避免频繁刷新分组与认证缓存
	carpoolQuotaChangeTolerance = 0.01

	// 整车（账号 7 天窗口）用量达到该百分比后，车内 manager 才能使用重置卡
	CarpoolResetCardThresholdPercent = 80.0
)

var (
	ErrCarpoolNotFound         = infraerrors.NotFound("CARPOOL_NOT_FOUND", "carpool not found")
	ErrCarpoolForbidden        = infraerrors.Forbidden("CARPOOL_FORBIDDEN", "only carpool managers can do this")
	ErrCarpoolAccountTaken     = infraerrors.Conflict("CARPOOL_ACCOUNT_TAKEN", "account already belongs to another carpool")
	ErrCarpoolAccountPlatform  = infraerrors.BadRequest("CARPOOL_ACCOUNT_PLATFORM", "carpool only supports openai accounts")
	ErrCarpoolInvalidSeats     = infraerrors.BadRequest("CARPOOL_INVALID_SEATS", "seats must be between 1 and 10")
	ErrCarpoolSeatsFull        = infraerrors.BadRequest("CARPOOL_SEATS_FULL", "carpool has no free seat")
	ErrCarpoolSeatsBelowCount  = infraerrors.BadRequest("CARPOOL_SEATS_BELOW_MEMBERS", "seats cannot be fewer than current members")
	ErrCarpoolInvalidQuota     = infraerrors.BadRequest("CARPOOL_INVALID_QUOTA", "manual weekly quota must be > 0")
	ErrCarpoolInvalidShare     = infraerrors.BadRequest("CARPOOL_INVALID_SHARE", "share percent must be within [0,100] and all seats together cannot exceed 100")
	ErrCarpoolResetNotAllowed  = infraerrors.BadRequest("CARPOOL_RESET_NOT_ALLOWED", "reset card can only be used after the carpool has used 80% of the 7-day window")
	ErrCarpoolResetUnsupported = infraerrors.BadRequest("CARPOOL_RESET_UNSUPPORTED", "reset cards are only available for OpenAI OAuth accounts")
	ErrCarpoolInvalidName      = infraerrors.BadRequest("CARPOOL_INVALID_NAME", "carpool name is required")
	ErrCarpoolInvalidAction    = infraerrors.BadRequest("CARPOOL_INVALID_ACTION", "invalid account action")
	ErrCarpoolMemberExists     = infraerrors.Conflict("CARPOOL_MEMBER_EXISTS", "user is already in this carpool")
	ErrCarpoolMemberNotFound   = infraerrors.NotFound("CARPOOL_MEMBER_NOT_FOUND", "carpool member not found")
	ErrCarpoolInvalidRole      = infraerrors.BadRequest("CARPOOL_INVALID_ROLE", "role must be manager or member")
)

type Carpool struct {
	ID             int64
	Name           string
	AccountID      int64
	GroupID        int64
	WeeklyQuotaUSD float64
	QuotaSource    string
	Seats          int
	ValidityDays   int
	Notes          string
	Status         string
	CreatedBy      *int64
	CreatedAt      time.Time
	UpdatedAt      time.Time

	// 预估状态，由 refreshEstimate 维护
	EstimatedWeeklyUSD  float64
	WindowResetAt       *time.Time
	BaselineAt          *time.Time
	BaselineUsedPercent float64

	MemberCount      int
	AllocatedPercent float64 // 已分配给各车位的百分比合计
}

// MemberWeeklyLimit 车位周额度 = 账号周额度 × 车位百分比；尚未预估时返回 nil（不限额）。
func (c *Carpool) MemberWeeklyLimit(sharePercent float64) *float64 {
	if c.WeeklyQuotaUSD <= 0 {
		return nil
	}
	limit := CarpoolShareWeeklyUSD(c.WeeklyQuotaUSD, sharePercent)
	return &limit
}

// DefaultSharePercent 新车位默认比例：剩余未分配比例均分给空车位（调用前 MemberCount 不含新成员）。
func (c *Carpool) DefaultSharePercent() float64 {
	free := c.Seats - c.MemberCount
	if free < 1 {
		free = 1
	}
	remaining := 100 - c.AllocatedPercent
	if remaining <= 0 {
		return 0
	}
	return math.Floor(remaining/float64(free)*100) / 100
}

type CarpoolMember struct {
	ID             int64
	CarpoolID      int64
	UserID         int64
	SubscriptionID *int64
	Role           string
	SharePercent   float64
	CreatedAt      time.Time

	Email    string
	Username string
}

// CarpoolMemberUsage 成员在拼车分组内的窗口用量（来自 usage_logs）。
type CarpoolMemberUsage struct {
	UserID       int64
	Requests     int64
	Tokens       int64
	StandardCost float64
	ActualCost   float64
	LastUsedAt   *time.Time
}

// CarpoolEstimateState 一次预估刷新要写回的状态。
type CarpoolEstimateState struct {
	EstimatedWeeklyUSD  float64
	WindowResetAt       *time.Time
	BaselineAt          *time.Time
	BaselineUsedPercent float64
	// ApplyToQuota 为 true 时同时更新生效额度（仅对 auto 来源生效）
	ApplyToQuota bool
}

type CarpoolRepository interface {
	Create(ctx context.Context, c *Carpool) error
	GetByID(ctx context.Context, id int64) (*Carpool, error)
	GetByAccountID(ctx context.Context, accountID int64) (*Carpool, error)
	List(ctx context.Context) ([]Carpool, error)
	ListByUser(ctx context.Context, userID int64) ([]Carpool, error)
	Update(ctx context.Context, c *Carpool) error
	SoftDelete(ctx context.Context, id int64) error
	// SaveEstimateState 写入预估状态；仅当库中窗口仍为 expectedWindowResetAt 时生效，
	// 多实例下只有一个实例能抢到新窗口，返回值表示是否写入成功。
	SaveEstimateState(ctx context.Context, id int64, state *CarpoolEstimateState, expectedWindowResetAt *time.Time) (bool, error)

	ListMembers(ctx context.Context, carpoolID int64) ([]CarpoolMember, error)
	GetMember(ctx context.Context, carpoolID, userID int64) (*CarpoolMember, error)
	AddMember(ctx context.Context, m *CarpoolMember) error
	UpdateMemberRole(ctx context.Context, carpoolID, userID int64, role string) error
	UpdateMemberShare(ctx context.Context, carpoolID, userID int64, sharePercent float64) error
	SetMemberSubscription(ctx context.Context, carpoolID, userID, subscriptionID int64) error
	RemoveMember(ctx context.Context, carpoolID, userID int64) error

	// ListLiveGroupSubscriptions 拼车分组下未撤销的订阅：user_id -> subscription_id
	ListLiveGroupSubscriptions(ctx context.Context, groupID int64) (map[int64]int64, error)
	// SetSubscriptionWeeklyLimit 写订阅级周限额覆盖，返回是否有变化
	SetSubscriptionWeeklyLimit(ctx context.Context, subscriptionID int64, limit *float64) (bool, error)
	// AlignSubscriptionWeeklyWindow 把订阅周窗口起点对齐到账号窗口起点 start（偏差小于 1 小时不动）；
	// 起点早于 start 的订阅属于上一个账号窗口，同时清零周用量。返回是否有变化。
	AlignSubscriptionWeeklyWindow(ctx context.Context, subscriptionID int64, start time.Time) (bool, error)

	// GetGroupUsageByUser 统计分组内每个用户自 since 起的用量。
	GetGroupUsageByUser(ctx context.Context, groupID int64, since time.Time) (map[int64]*CarpoolMemberUsage, error)
	// GetAccountActualCostSince 账号自 since 起的 actual_cost 合计，与成员订阅扣费同口径（已含分组模型定价与倍率）。
	GetAccountActualCostSince(ctx context.Context, accountID int64, since time.Time) (float64, error)
}

// CarpoolEstimateWeekly 由样本反推账号周额度：样本成本 ÷ 样本期间窗口上涨比例。
// 上涨太少时样本不可靠，返回 ok=false。
func CarpoolEstimateWeekly(sampleCostUSD, sampleUsedPercent float64) (float64, bool) {
	if sampleCostUSD <= 0 || sampleUsedPercent < CarpoolMinEstimatePercent {
		return 0, false
	}
	return roundCarpoolUSD(sampleCostUSD / (sampleUsedPercent / 100)), true
}

// CarpoolShareWeeklyUSD 车位周额度 = 账号周额度 × 车位百分比。
func CarpoolShareWeeklyUSD(weeklyQuotaUSD, sharePercent float64) float64 {
	if weeklyQuotaUSD <= 0 || sharePercent <= 0 {
		return 0
	}
	return roundCarpoolUSD(weeklyQuotaUSD * sharePercent / 100)
}

func roundCarpoolUSD(v float64) float64 {
	return math.Round(v*10000) / 10000
}

func validateCarpoolSeats(seats int) error {
	if seats < CarpoolMinSeats || seats > CarpoolMaxSeats {
		return ErrCarpoolInvalidSeats
	}
	return nil
}

func validateCarpoolShare(share float64) error {
	if share < 0 || share > 100 || math.IsNaN(share) {
		return ErrCarpoolInvalidShare
	}
	return nil
}

func normalizeCarpoolRole(role string) (string, error) {
	switch role {
	case "":
		return CarpoolRoleManager, nil
	case CarpoolRoleManager, CarpoolRoleMember:
		return role, nil
	default:
		return "", ErrCarpoolInvalidRole
	}
}

// carpoolGroupWeeklyLimit 分组周限额只作兜底（各车位的实际额度写在订阅覆盖上）：
// 有预估时取整车额度，尚未预估时取负数让分组暂不限额，而不是 0（0 表示禁止使用）。
func carpoolGroupWeeklyLimit(c *Carpool) float64 {
	if c.WeeklyQuotaUSD > 0 {
		return c.WeeklyQuotaUSD
	}
	return -1
}

// carpoolWindowSample 账号上游 7 天窗口的当前快照。
type carpoolWindowSample struct {
	ResetAt     time.Time
	UsedPercent float64
}

// carpoolWindowFromAccount 取账号 7 天窗口快照；没有快照或窗口已过期（等下一次请求刷新）时返回 nil。
func carpoolWindowFromAccount(account *Account, now time.Time) *carpoolWindowSample {
	if account == nil {
		return nil
	}
	seven := buildCodexUsageProgressFromExtra(account.Extra, "7d", now)
	if seven == nil || seven.ResetsAt == nil || !now.Before(*seven.ResetsAt) {
		return nil
	}
	return &carpoolWindowSample{ResetAt: *seven.ResetsAt, UsedPercent: seven.Utilization}
}

// isNewWindow 判断快照是否属于还没跟踪过的窗口。
func (w *carpoolWindowSample) isNewWindow(c *Carpool) bool {
	if c.WindowResetAt == nil {
		return true
	}
	diff := w.ResetAt.Sub(*c.WindowResetAt)
	return diff > carpoolWindowResetTolerance || diff < -carpoolWindowResetTolerance
}

// carpoolQuotaChanged 判断预估变化是否大到值得更新生效额度。
func carpoolQuotaChanged(current, estimated float64) bool {
	if current <= 0 {
		return estimated > 0
	}
	return math.Abs(estimated-current)/current >= carpoolQuotaChangeTolerance
}
