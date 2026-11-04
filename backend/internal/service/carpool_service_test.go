package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCarpoolEstimateWeekly(t *testing.T) {
	weekly, ok := CarpoolEstimateWeekly(30, 25)
	require.True(t, ok)
	require.InDelta(t, 120, weekly, 1e-9)

	_, ok = CarpoolEstimateWeekly(1, CarpoolMinEstimatePercent-0.1)
	require.False(t, ok, "样本太小不预估")
	_, ok = CarpoolEstimateWeekly(0, 50)
	require.False(t, ok)
}

func TestCarpoolPerSeatWeeklyUSD(t *testing.T) {
	require.InDelta(t, 50, CarpoolPerSeatWeeklyUSD(200, 0, 4), 1e-9)
	require.InDelta(t, 60, CarpoolPerSeatWeeklyUSD(200, 0.1, 3), 1e-9)
	require.Zero(t, CarpoolPerSeatWeeklyUSD(200, 0, 0))
	require.Zero(t, CarpoolPerSeatWeeklyUSD(0, 0, 3), "尚未预估")
}

func TestCarpoolGroupWeeklyLimit_NoEstimateIsUnlimitedNotZero(t *testing.T) {
	require.Negative(t, carpoolGroupWeeklyLimit(&Carpool{Seats: 3}), "0 会禁止使用，未预估时必须是不限额")
	require.InDelta(t, 40, carpoolGroupWeeklyLimit(&Carpool{Seats: 3, WeeklyQuotaUSD: 120}), 1e-9)
}

func TestApplyCarpoolQuotaInput(t *testing.T) {
	c := &Carpool{WeeklyQuotaUSD: 100, EstimatedWeeklyUSD: 100, QuotaSource: CarpoolQuotaSourceAuto, Seats: 2}

	manual := 300.0
	require.NoError(t, applyCarpoolQuotaInput(c, &UpdateCarpoolQuotaInput{WeeklyQuotaUSD: &manual}))
	require.Equal(t, 300.0, c.WeeklyQuotaUSD)
	require.Equal(t, CarpoolQuotaSourceManual, c.QuotaSource)

	auto := CarpoolQuotaSourceAuto
	require.NoError(t, applyCarpoolQuotaInput(c, &UpdateCarpoolQuotaInput{QuotaSource: &auto}))
	require.Equal(t, 100.0, c.WeeklyQuotaUSD, "恢复自动时回到最近的预估值")
	require.Equal(t, CarpoolQuotaSourceAuto, c.QuotaSource)

	bad := 1.0
	require.ErrorIs(t, applyCarpoolQuotaInput(c, &UpdateCarpoolQuotaInput{ReserveRatio: &bad}), ErrCarpoolInvalidQuota)
	zero := 0.0
	require.ErrorIs(t, applyCarpoolQuotaInput(c, &UpdateCarpoolQuotaInput{WeeklyQuotaUSD: &zero}), ErrCarpoolInvalidQuota)
}

func TestMaskCarpoolEmail(t *testing.T) {
	require.Equal(t, "al***@example.com", maskCarpoolEmail("alice@example.com"))
	require.Equal(t, "a***@x.com", maskCarpoolEmail("ab@x.com"))
	require.Equal(t, "nomail", maskCarpoolEmail("nomail"))
}

// ---------- 假实现 ----------

type fakeCarpoolRepo struct {
	carpools    map[int64]*Carpool
	members     map[int64][]CarpoolMember
	accountCost float64
	costSince   time.Time
	saveAllowed bool
}

func (r *fakeCarpoolRepo) Create(context.Context, *Carpool) error { return nil }
func (r *fakeCarpoolRepo) GetByID(_ context.Context, id int64) (*Carpool, error) {
	c, ok := r.carpools[id]
	if !ok {
		return nil, ErrCarpoolNotFound
	}
	cp := *c
	cp.MemberCount = len(r.members[id])
	return &cp, nil
}
func (r *fakeCarpoolRepo) GetByAccountID(context.Context, int64) (*Carpool, error) {
	return nil, ErrCarpoolNotFound
}
func (r *fakeCarpoolRepo) List(context.Context) ([]Carpool, error) { return nil, nil }
func (r *fakeCarpoolRepo) ListByUser(_ context.Context, userID int64) ([]Carpool, error) {
	var out []Carpool
	for id, ms := range r.members {
		for _, m := range ms {
			if m.UserID == userID {
				out = append(out, *r.carpools[id])
			}
		}
	}
	return out, nil
}
func (r *fakeCarpoolRepo) Update(_ context.Context, c *Carpool) error {
	cp := *c
	r.carpools[c.ID] = &cp
	return nil
}
func (r *fakeCarpoolRepo) SoftDelete(context.Context, int64) error { return nil }
func (r *fakeCarpoolRepo) SaveEstimateState(_ context.Context, id int64, state *CarpoolEstimateState, _ *time.Time) (bool, error) {
	if !r.saveAllowed {
		return false, nil
	}
	c := r.carpools[id]
	c.EstimatedWeeklyUSD = state.EstimatedWeeklyUSD
	c.WindowResetAt = state.WindowResetAt
	c.BaselineAt = state.BaselineAt
	c.BaselineUsedPercent = state.BaselineUsedPercent
	if state.ApplyToQuota && c.QuotaSource == CarpoolQuotaSourceAuto {
		c.WeeklyQuotaUSD = state.EstimatedWeeklyUSD
	}
	return true, nil
}
func (r *fakeCarpoolRepo) ListMembers(_ context.Context, id int64) ([]CarpoolMember, error) {
	return r.members[id], nil
}
func (r *fakeCarpoolRepo) GetMember(_ context.Context, id, userID int64) (*CarpoolMember, error) {
	for _, m := range r.members[id] {
		if m.UserID == userID {
			cp := m
			return &cp, nil
		}
	}
	return nil, ErrCarpoolMemberNotFound
}
func (r *fakeCarpoolRepo) AddMember(context.Context, *CarpoolMember) error              { return nil }
func (r *fakeCarpoolRepo) UpdateMemberRole(context.Context, int64, int64, string) error { return nil }
func (r *fakeCarpoolRepo) SetMemberSubscription(context.Context, int64, int64, int64) error {
	return nil
}
func (r *fakeCarpoolRepo) RemoveMember(context.Context, int64, int64) error { return nil }
func (r *fakeCarpoolRepo) GetGroupUsageByUser(context.Context, int64, time.Time) (map[int64]*CarpoolMemberUsage, error) {
	return map[int64]*CarpoolMemberUsage{1: {UserID: 1, Requests: 3, ActualCost: 1.5}}, nil
}
func (r *fakeCarpoolRepo) GetAccountActualCostSince(_ context.Context, _ int64, since time.Time) (float64, error) {
	r.costSince = since
	return r.accountCost, nil
}

type fakeCarpoolAdmin struct {
	AdminService
	account     *Account
	groupWeekly map[int64]*float64
	groupInputs []*UpdateGroupInput
	schedulable *bool
}

func (a *fakeCarpoolAdmin) GetAccount(context.Context, int64) (*Account, error) {
	return a.account, nil
}

func (a *fakeCarpoolAdmin) GetGroup(_ context.Context, id int64) (*Group, error) {
	return &Group{ID: id, WeeklyLimitUSD: a.groupWeekly[id]}, nil
}

func (a *fakeCarpoolAdmin) UpdateGroup(_ context.Context, id int64, in *UpdateGroupInput) (*Group, error) {
	a.groupInputs = append(a.groupInputs, in)
	a.groupWeekly[id] = normalizeLimit(in.WeeklyLimitUSD)
	return &Group{ID: id}, nil
}

func (a *fakeCarpoolAdmin) SetAccountSchedulable(_ context.Context, _ int64, v bool) (*Account, error) {
	a.schedulable = &v
	return a.account, nil
}

func (a *fakeCarpoolAdmin) setWindow(usedPercent float64, resetAt time.Time) {
	a.account.Extra = map[string]any{
		"codex_7d_used_percent": usedPercent,
		"codex_7d_reset_at":     resetAt.Format(time.RFC3339),
	}
}

func newCarpoolTestService() (*CarpoolService, *fakeCarpoolRepo, *fakeCarpoolAdmin, *[]int64) {
	repo := &fakeCarpoolRepo{
		saveAllowed: true,
		carpools: map[int64]*Carpool{
			10: {ID: 10, Name: "A", AccountID: 100, GroupID: 1000, QuotaSource: CarpoolQuotaSourceAuto, Seats: 4},
			20: {ID: 20, Name: "B", AccountID: 200, GroupID: 2000, QuotaSource: CarpoolQuotaSourceAuto, Seats: 2},
		},
		members: map[int64][]CarpoolMember{
			10: {
				{CarpoolID: 10, UserID: 1, Role: CarpoolRoleManager, Email: "alice@example.com"},
				{CarpoolID: 10, UserID: 2, Role: CarpoolRoleMember, Email: "bob@example.com"},
			},
			20: {{CarpoolID: 20, UserID: 3, Role: CarpoolRoleManager, Email: "carol@example.com"}},
		},
	}
	admin := &fakeCarpoolAdmin{
		account:     &Account{ID: 100, Name: "acc", Platform: "openai"},
		groupWeekly: map[int64]*float64{},
	}
	svc := NewCarpoolService(repo, admin, nil, nil, nil, nil)
	resets := &[]int64{}
	svc.resetMemberWeekly = func(_ context.Context, id int64) error {
		*resets = append(*resets, id)
		return nil
	}
	return svc, repo, admin, resets
}

func refreshCarpool(t *testing.T, svc *CarpoolService, repo *fakeCarpoolRepo, id int64, now time.Time) *Carpool {
	t.Helper()
	c, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.NoError(t, svc.refreshEstimate(context.Background(), c, now))
	c, err = repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	return c
}

// ---------- 预估 ----------

func TestRefreshEstimate_ExcludesUsageBeforeCarpool(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	now := time.Now()
	resetAt := now.Add(3 * 24 * time.Hour)

	// 建车时账号已用 40%（可能不是经本站用的），不能算进样本
	admin.setWindow(40, resetAt)
	repo.accountCost = 2
	c := refreshCarpool(t, svc, repo, 10, now)
	require.Zero(t, c.WeeklyQuotaUSD, "基线刚建立，还没有样本")
	require.Equal(t, 40.0, c.BaselineUsedPercent)
	require.Equal(t, now, *c.BaselineAt)
	require.Nil(t, admin.groupWeekly[1000], "尚未预估时分组不限额")

	// 增量不足 5% 仍不预估
	admin.setWindow(44, resetAt)
	c = refreshCarpool(t, svc, repo, 10, now.Add(time.Hour))
	require.Zero(t, c.EstimatedWeeklyUSD)

	// 上涨 10% 花了 $30 → 周额度 $300，每人 $75；成本从基线起算
	admin.setWindow(50, resetAt)
	repo.accountCost = 30
	c = refreshCarpool(t, svc, repo, 10, now.Add(2*time.Hour))
	require.InDelta(t, 300, c.EstimatedWeeklyUSD, 1e-9)
	require.InDelta(t, 300, c.WeeklyQuotaUSD, 1e-9)
	require.Equal(t, now, repo.costSince)
	require.InDelta(t, 75, *admin.groupWeekly[1000], 1e-9)
}

func TestRefreshEstimate_ConvergesWithUsage(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	now := time.Now()
	resetAt := now.Add(6 * 24 * time.Hour)
	admin.setWindow(0, resetAt)
	refreshCarpool(t, svc, repo, 10, now)

	admin.setWindow(10, resetAt)
	repo.accountCost = 100
	c := refreshCarpool(t, svc, repo, 10, now.Add(time.Hour))
	require.InDelta(t, 1000, c.WeeklyQuotaUSD, 1e-9)

	// 样本变大后预估跟着调整
	admin.setWindow(50, resetAt)
	repo.accountCost = 678
	c = refreshCarpool(t, svc, repo, 10, now.Add(2*time.Hour))
	require.InDelta(t, 1356, c.WeeklyQuotaUSD, 1e-9)

	// 变化不到 1% 不改额度，也不刷新分组
	updates := len(admin.groupInputs)
	repo.accountCost = 679
	c = refreshCarpool(t, svc, repo, 10, now.Add(3*time.Hour))
	require.InDelta(t, 1358, c.EstimatedWeeklyUSD, 1e-9)
	require.InDelta(t, 1356, c.WeeklyQuotaUSD, 1e-9)
	require.Len(t, admin.groupInputs, updates)
}

func TestRefreshEstimate_NewWindowKeepsQuotaAndAlignsMembers(t *testing.T) {
	svc, repo, admin, resets := newCarpoolTestService()
	sub := int64(501)
	repo.members[10][0].SubscriptionID = &sub
	now := time.Now()
	resetAt := now.Add(time.Hour)
	admin.setWindow(10, resetAt)
	refreshCarpool(t, svc, repo, 10, now)
	admin.setWindow(60, resetAt)
	repo.accountCost = 500
	c := refreshCarpool(t, svc, repo, 10, now.Add(time.Minute))
	require.InDelta(t, 1000, c.WeeklyQuotaUSD, 1e-9)
	require.Empty(t, *resets, "同一窗口不清零成员用量")

	// 重置时间抖动不算新窗口
	admin.setWindow(60, resetAt.Add(10*time.Minute))
	refreshCarpool(t, svc, repo, 10, now.Add(2*time.Minute))
	require.Empty(t, *resets)

	// 进入新窗口：沿用上次预估，基线回到窗口起点，成员周用量清零
	later := resetAt.Add(time.Hour)
	nextReset := resetAt.Add(7 * 24 * time.Hour)
	admin.setWindow(1, nextReset)
	c = refreshCarpool(t, svc, repo, 10, later)
	require.InDelta(t, 1000, c.WeeklyQuotaUSD, 1e-9, "新窗口样本不足时沿用上次预估")
	require.Zero(t, c.BaselineUsedPercent)
	require.Equal(t, resetAt.Truncate(time.Second).UTC(), c.BaselineAt.Truncate(time.Second).UTC())
	require.Equal(t, []int64{501}, *resets)
}

func TestRefreshEstimate_OtherInstanceWonWindow(t *testing.T) {
	svc, repo, admin, resets := newCarpoolTestService()
	sub := int64(501)
	repo.members[10][0].SubscriptionID = &sub
	past := time.Now()
	repo.carpools[10].WindowResetAt = &past
	admin.setWindow(1, past.Add(7*24*time.Hour))
	repo.saveAllowed = false

	refreshCarpool(t, svc, repo, 10, past.Add(time.Minute))
	require.Empty(t, *resets, "没抢到窗口不能重复清零成员用量")
}

func TestRefreshEstimate_ManualQuotaNotOverwritten(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	repo.carpools[10].QuotaSource = CarpoolQuotaSourceManual
	repo.carpools[10].WeeklyQuotaUSD = 800
	now := time.Now()
	resetAt := now.Add(24 * time.Hour)
	admin.setWindow(0, resetAt)
	refreshCarpool(t, svc, repo, 10, now)
	admin.setWindow(50, resetAt)
	repo.accountCost = 100

	c := refreshCarpool(t, svc, repo, 10, now.Add(time.Hour))
	require.InDelta(t, 200, c.EstimatedWeeklyUSD, 1e-9, "手动模式也继续预估，方便对比")
	require.InDelta(t, 800, c.WeeklyQuotaUSD, 1e-9)
	require.InDelta(t, 200, *admin.groupWeekly[1000], 1e-9, "每人 800/4")
}

func TestRefreshEstimate_ExpiredOrMissingSnapshotKeepsState(t *testing.T) {
	svc, repo, admin, resets := newCarpoolTestService()
	repo.carpools[10].WeeklyQuotaUSD = 400
	repo.carpools[10].EstimatedWeeklyUSD = 400
	limit := 100.0
	admin.groupWeekly[1000] = &limit
	now := time.Now()

	c := refreshCarpool(t, svc, repo, 10, now)
	require.InDelta(t, 400, c.WeeklyQuotaUSD, 1e-9, "没有快照")

	admin.setWindow(90, now.Add(-time.Minute))
	c = refreshCarpool(t, svc, repo, 10, now)
	require.InDelta(t, 400, c.WeeklyQuotaUSD, 1e-9, "快照窗口已过期")
	require.Nil(t, c.WindowResetAt)
	require.Empty(t, *resets)
}

// ---------- 分组同步 ----------

func TestSyncGroupLimit_OnlyTouchesWeeklyLimit(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	weekly := 600.0
	_, err := svc.ManagerUpdateQuota(context.Background(), 10, 1, &UpdateCarpoolQuotaInput{WeeklyQuotaUSD: &weekly})
	require.NoError(t, err)

	require.Len(t, admin.groupInputs, 1)
	// 只设周限额，其余字段保持零值 = 不修改，模型定价等分组设置不受影响
	require.Equal(t, &UpdateGroupInput{WeeklyLimitUSD: admin.groupInputs[0].WeeklyLimitUSD}, admin.groupInputs[0])
	require.InDelta(t, 150, *admin.groupInputs[0].WeeklyLimitUSD, 1e-9)

	// 分组页手动改了周限额，下一轮被拼车修正
	tampered := 1.0
	admin.groupWeekly[1000] = &tampered
	repo.carpools[10].MemberCount = 2
	refreshCarpool(t, svc, repo, 10, time.Now())
	require.InDelta(t, 150, *admin.groupWeekly[1000], 1e-9)
}

// ---------- 权限与可见性 ----------

func TestCarpoolDetail_OtherCarpoolInvisible(t *testing.T) {
	svc, _, _, _ := newCarpoolTestService()
	ctx := context.Background()

	_, err := svc.GetDetail(ctx, 20, 1)
	require.ErrorIs(t, err, ErrCarpoolNotFound, "非成员看不到别的拼车")

	mine, err := svc.ListMine(ctx, 1)
	require.NoError(t, err)
	require.Len(t, mine, 1)
	require.Equal(t, int64(10), mine[0].ID)
	require.Equal(t, CarpoolRoleManager, mine[0].MyRole)
}

func TestCarpoolDetail_MembersSeeEachOther(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	now := time.Now()
	resetAt := now.Add(3 * 24 * time.Hour)
	repo.carpools[10].WeeklyQuotaUSD = 400
	repo.carpools[10].BaselineAt = &now
	repo.carpools[10].BaselineUsedPercent = 5
	repo.carpools[10].WindowResetAt = &resetAt
	admin.setWindow(25, resetAt)
	repo.accountCost = 30

	detail, err := svc.GetDetail(context.Background(), 10, 2)
	require.NoError(t, err)
	require.Len(t, detail.Members, 2)
	require.Equal(t, "al***@example.com", detail.Members[0].Email, "他人邮箱打码")
	require.Equal(t, "bob@example.com", detail.Members[1].Email, "自己邮箱完整显示")
	require.True(t, detail.Members[1].IsMe)
	require.Equal(t, int64(3), detail.Members[0].Requests7d)
	require.InDelta(t, 100, detail.Members[0].WeeklyLimitUSD, 1e-9)
	require.Equal(t, resetAt.Unix(), detail.Members[0].WeeklyResetsAt.Unix(), "成员重置时间跟随账号窗口")

	require.InDelta(t, 20, detail.Estimate.SampleUsedPercent, 1e-9)
	require.InDelta(t, 30, detail.Estimate.SampleCostUSD, 1e-9)
}

func TestCarpoolManagerPermissions(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	ctx := context.Background()
	weekly := 600.0

	_, err := svc.ManagerUpdateQuota(ctx, 10, 2, &UpdateCarpoolQuotaInput{WeeklyQuotaUSD: &weekly})
	require.ErrorIs(t, err, ErrCarpoolForbidden, "只读成员不能改额度")

	_, err = svc.ManagerUpdateQuota(ctx, 20, 1, &UpdateCarpoolQuotaInput{WeeklyQuotaUSD: &weekly})
	require.ErrorIs(t, err, ErrCarpoolNotFound, "不能改别的拼车")

	view, err := svc.ManagerUpdateQuota(ctx, 10, 1, &UpdateCarpoolQuotaInput{WeeklyQuotaUSD: &weekly})
	require.NoError(t, err)
	require.InDelta(t, 150, view.PerSeatWeeklyUSD, 1e-9)
	require.Equal(t, CarpoolQuotaSourceManual, repo.carpools[10].QuotaSource)

	on := false
	require.ErrorIs(t, svc.AccountAction(ctx, 10, 2, CarpoolAccountActionSetSchedulable, &on), ErrCarpoolForbidden)
	require.NoError(t, svc.AccountAction(ctx, 10, 1, CarpoolAccountActionSetSchedulable, &on))
	require.NotNil(t, admin.schedulable)
	require.False(t, *admin.schedulable)
}
