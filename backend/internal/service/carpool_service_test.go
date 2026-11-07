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

func TestCarpoolShareWeeklyUSD(t *testing.T) {
	require.InDelta(t, 50, CarpoolShareWeeklyUSD(200, 25), 1e-9)
	require.InDelta(t, 120, CarpoolShareWeeklyUSD(200, 60), 1e-9)
	require.Zero(t, CarpoolShareWeeklyUSD(200, 0))
	require.Zero(t, CarpoolShareWeeklyUSD(0, 50), "尚未预估")

	c := &Carpool{WeeklyQuotaUSD: 0}
	require.Nil(t, c.MemberWeeklyLimit(50), "尚未预估时车位不限额")
	c.WeeklyQuotaUSD = 300
	require.InDelta(t, 90, *c.MemberWeeklyLimit(30), 1e-9)
	require.Zero(t, *c.MemberWeeklyLimit(0), "0% 车位没有额度，而不是回落成不限额")
}

func TestCarpoolDefaultSharePercent(t *testing.T) {
	c := &Carpool{Seats: 3}
	require.Equal(t, 33.33, c.DefaultSharePercent())
	c.MemberCount, c.AllocatedPercent = 2, 66.66
	require.Equal(t, 33.34, c.DefaultSharePercent(), "最后一个空位拿走剩余比例")
	c.MemberCount, c.AllocatedPercent = 1, 70
	require.Equal(t, 15.0, c.DefaultSharePercent())
	c.MemberCount, c.AllocatedPercent = 3, 100
	require.Zero(t, c.DefaultSharePercent(), "没有剩余比例")
}

func TestEffectiveWeeklyLimit(t *testing.T) {
	groupLimit := 100.0
	group := &Group{WeeklyLimitUSD: &groupLimit}
	override := 30.0
	zero := 0.0
	require.Equal(t, 30.0, *EffectiveWeeklyLimit(&override, group))
	require.Equal(t, 0.0, *EffectiveWeeklyLimit(&zero, group), "0 覆盖表示本周无额度")
	require.Equal(t, 100.0, *EffectiveWeeklyLimit(nil, group))
	require.Nil(t, EffectiveWeeklyLimit(nil, &Group{}))

	sub := &UserSubscription{WeeklyUsageUSD: 29, WeeklyLimitUSD: &override}
	require.True(t, sub.CheckWeeklyLimit(group, 1))
	require.False(t, sub.CheckWeeklyLimit(group, 2), "按订阅覆盖而不是分组限额拦截")
}

func TestCarpoolGroupWeeklyLimit_NoEstimateIsUnlimitedNotZero(t *testing.T) {
	require.Negative(t, carpoolGroupWeeklyLimit(&Carpool{Seats: 3}), "0 会禁止使用，未预估时必须是不限额")
	require.InDelta(t, 120, carpoolGroupWeeklyLimit(&Carpool{Seats: 3, WeeklyQuotaUSD: 120}), 1e-9)
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

	zero := 0.0
	require.ErrorIs(t, applyCarpoolQuotaInput(c, &UpdateCarpoolQuotaInput{WeeklyQuotaUSD: &zero}), ErrCarpoolInvalidQuota)
}

func TestMaskCarpoolEmail(t *testing.T) {
	require.Equal(t, "al***@example.com", maskCarpoolEmail("alice@example.com"))
	require.Equal(t, "a***@x.com", maskCarpoolEmail("ab@x.com"))
	require.Equal(t, "nomail", maskCarpoolEmail("nomail"))
}

func TestOpenAIWeeklyUsedPercent(t *testing.T) {
	_, ok := openAIWeeklyUsedPercent(nil)
	require.False(t, ok)
	used, ok := openAIWeeklyUsedPercent(&OpenAIRateLimit{
		PrimaryWindow:   &OpenAIRateLimitWindow{UsedPercent: 90, LimitWindowSeconds: 5 * 3600},
		SecondaryWindow: &OpenAIRateLimitWindow{UsedPercent: 82, LimitWindowSeconds: 7 * 24 * 3600},
	})
	require.True(t, ok)
	require.Equal(t, 82.0, used, "取 7 天窗口，不是 5 小时窗口")
}

// ---------- 假实现 ----------

type fakeCarpoolSub struct {
	userID      int64
	groupID     int64
	limit       *float64
	windowStart *time.Time
	usage       float64
}

type fakeCarpoolRepo struct {
	carpools    map[int64]*Carpool
	members     map[int64][]CarpoolMember
	subs        map[int64]*fakeCarpoolSub
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
	cp.AllocatedPercent = 0
	for _, m := range r.members[id] {
		cp.AllocatedPercent += m.SharePercent
	}
	return &cp, nil
}
func (r *fakeCarpoolRepo) GetByAccountID(context.Context, int64) (*Carpool, error) {
	return nil, ErrCarpoolNotFound
}
func (r *fakeCarpoolRepo) List(context.Context) ([]Carpool, error) { return nil, nil }
func (r *fakeCarpoolRepo) ListByUser(_ context.Context, userID int64) ([]Carpool, error) {
	var out []Carpool
	for id, c := range r.carpools {
		hit := false
		for _, m := range r.members[id] {
			hit = hit || m.UserID == userID
		}
		for _, s := range r.subs {
			hit = hit || (s.userID == userID && s.groupID == c.GroupID)
		}
		if hit {
			cp, _ := r.GetByID(context.Background(), id)
			out = append(out, *cp)
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
	return append([]CarpoolMember(nil), r.members[id]...), nil
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
func (r *fakeCarpoolRepo) AddMember(_ context.Context, m *CarpoolMember) error {
	if _, err := r.GetMember(context.Background(), m.CarpoolID, m.UserID); err == nil {
		return ErrCarpoolMemberExists
	}
	r.members[m.CarpoolID] = append(r.members[m.CarpoolID], *m)
	return nil
}
func (r *fakeCarpoolRepo) mutateMember(id, userID int64, fn func(*CarpoolMember)) error {
	for i := range r.members[id] {
		if r.members[id][i].UserID == userID {
			fn(&r.members[id][i])
			return nil
		}
	}
	return ErrCarpoolMemberNotFound
}
func (r *fakeCarpoolRepo) UpdateMemberRole(_ context.Context, id, userID int64, role string) error {
	return r.mutateMember(id, userID, func(m *CarpoolMember) { m.Role = role })
}
func (r *fakeCarpoolRepo) UpdateMemberShare(_ context.Context, id, userID int64, share float64) error {
	return r.mutateMember(id, userID, func(m *CarpoolMember) { m.SharePercent = share })
}
func (r *fakeCarpoolRepo) SetMemberSubscription(_ context.Context, id, userID, subID int64) error {
	return r.mutateMember(id, userID, func(m *CarpoolMember) { m.SubscriptionID = &subID })
}
func (r *fakeCarpoolRepo) RemoveMember(_ context.Context, id, userID int64) error {
	ms := r.members[id]
	for i := range ms {
		if ms[i].UserID == userID {
			r.members[id] = append(ms[:i:i], ms[i+1:]...)
			return nil
		}
	}
	return ErrCarpoolMemberNotFound
}
func (r *fakeCarpoolRepo) ListLiveGroupSubscriptions(_ context.Context, groupID int64) (map[int64]int64, error) {
	out := map[int64]int64{}
	for id, s := range r.subs {
		if s.groupID == groupID {
			out[s.userID] = id
		}
	}
	return out, nil
}
func (r *fakeCarpoolRepo) SetSubscriptionWeeklyLimit(_ context.Context, subID int64, limit *float64) (bool, error) {
	s := r.subs[subID]
	if (s.limit == nil && limit == nil) || (s.limit != nil && limit != nil && *s.limit == *limit) {
		return false, nil
	}
	s.limit = limit
	return true, nil
}
func (r *fakeCarpoolRepo) AlignSubscriptionWeeklyWindow(_ context.Context, subID int64, start time.Time) (bool, error) {
	s := r.subs[subID]
	if s.windowStart != nil {
		diff := s.windowStart.Sub(start)
		if diff <= time.Hour && diff >= -time.Hour {
			return false, nil
		}
	}
	if s.windowStart == nil || s.windowStart.Before(start.Add(-time.Hour)) {
		s.usage = 0
	}
	s.windowStart = &start
	return true, nil
}
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

// newCarpoolTestService 拼车 10（4 座）有 alice=manager 40%、bob=member 30% 两个车位，各自绑定订阅；
// 拼车 20 是另一辆车。
func newCarpoolTestService() (*CarpoolService, *fakeCarpoolRepo, *fakeCarpoolAdmin, *[]int64) {
	repo := &fakeCarpoolRepo{
		saveAllowed: true,
		carpools: map[int64]*Carpool{
			10: {ID: 10, Name: "A", AccountID: 100, GroupID: 1000, QuotaSource: CarpoolQuotaSourceAuto, Seats: 4},
			20: {ID: 20, Name: "B", AccountID: 200, GroupID: 2000, QuotaSource: CarpoolQuotaSourceAuto, Seats: 2},
		},
		members: map[int64][]CarpoolMember{
			10: {
				{CarpoolID: 10, UserID: 1, Role: CarpoolRoleManager, Email: "alice@example.com", SharePercent: 40, SubscriptionID: i64(501)},
				{CarpoolID: 10, UserID: 2, Role: CarpoolRoleMember, Email: "bob@example.com", SharePercent: 30, SubscriptionID: i64(502)},
			},
			20: {{CarpoolID: 20, UserID: 3, Role: CarpoolRoleManager, Email: "carol@example.com", SharePercent: 100, SubscriptionID: i64(503)}},
		},
		subs: map[int64]*fakeCarpoolSub{
			501: {userID: 1, groupID: 1000},
			502: {userID: 2, groupID: 1000},
			503: {userID: 3, groupID: 2000},
		},
	}
	admin := &fakeCarpoolAdmin{
		account:     &Account{ID: 100, Name: "acc", Platform: "openai"},
		groupWeekly: map[int64]*float64{},
	}
	svc := NewCarpoolService(repo, admin, nil, nil, nil, nil, nil)
	invalidated := &[]int64{}
	svc.invalidateSub = func(userID, _ int64) { *invalidated = append(*invalidated, userID) }
	return svc, repo, admin, invalidated
}

func syncCarpool(t *testing.T, svc *CarpoolService, repo *fakeCarpoolRepo, id int64, now time.Time) *Carpool {
	t.Helper()
	c, err := repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	require.NoError(t, svc.syncLocked(context.Background(), c, now))
	c, err = repo.GetByID(context.Background(), id)
	require.NoError(t, err)
	return c
}

// ---------- 车位 ↔ 订阅 ----------

func TestReconcile_SubscriptionAssignedOutsideCarpoolGetsSeat(t *testing.T) {
	svc, repo, _, _ := newCarpoolTestService()
	// 管理员在订阅页直接给 dave 分配了拼车分组订阅
	repo.subs[504] = &fakeCarpoolSub{userID: 4, groupID: 1000}

	mine, err := svc.ListMine(context.Background(), 4)
	require.NoError(t, err)
	require.Len(t, mine, 1, "分配了拼车分组订阅就能在我的拼车里看到")
	require.Equal(t, int64(10), mine[0].ID)

	m, err := repo.GetMember(context.Background(), 10, 4)
	require.NoError(t, err)
	require.Equal(t, int64(504), *m.SubscriptionID)
	require.Equal(t, 15.0, m.SharePercent, "剩余 30% 均分给 2 个空位")
}

func TestReconcile_ExtraSubscriptionsAddSeatsWithRemainingShare(t *testing.T) {
	svc, repo, _, _ := newCarpoolTestService()
	repo.carpools[10].Seats = 2
	repo.members[10][0].SharePercent = 60
	repo.members[10][1].SharePercent = 40
	repo.subs[504] = &fakeCarpoolSub{userID: 4, groupID: 1000}

	c := syncCarpool(t, svc, repo, 10, time.Now())
	require.Equal(t, 3, c.Seats, "超出座位数时自动加座")
	m, err := repo.GetMember(context.Background(), 10, 4)
	require.NoError(t, err)
	require.Zero(t, m.SharePercent, "比例已分完，新车位为 0 等管理员分配")
}

func TestReconcile_RevokedSubscriptionReleasesSeat(t *testing.T) {
	svc, repo, _, _ := newCarpoolTestService()
	delete(repo.subs, 502)

	syncCarpool(t, svc, repo, 10, time.Now())
	_, err := repo.GetMember(context.Background(), 10, 2)
	require.ErrorIs(t, err, ErrCarpoolMemberNotFound)

	_, err = svc.GetDetail(context.Background(), 10, 2)
	require.ErrorIs(t, err, ErrCarpoolNotFound, "订阅撤销后看不到拼车")
}

// ---------- 车位比例 ----------

func TestSyncLimits_WritesShareBasedSubscriptionLimits(t *testing.T) {
	svc, repo, admin, invalidated := newCarpoolTestService()
	repo.carpools[10].WeeklyQuotaUSD = 300
	repo.carpools[10].QuotaSource = CarpoolQuotaSourceManual

	syncCarpool(t, svc, repo, 10, time.Now())
	require.InDelta(t, 120, *repo.subs[501].limit, 1e-9, "40% × 300")
	require.InDelta(t, 90, *repo.subs[502].limit, 1e-9, "30% × 300")
	require.InDelta(t, 300, *admin.groupWeekly[1000], 1e-9, "分组只作整车兜底")
	require.ElementsMatch(t, []int64{1, 2}, *invalidated, "改了订阅限额要失效缓存")

	*invalidated = nil
	syncCarpool(t, svc, repo, 10, time.Now())
	require.Empty(t, *invalidated, "没有变化不重复失效缓存")
}

func TestReserveRatio_ComesOffTheTopBeforeShares(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	repo.carpools[10].WeeklyQuotaUSD = 1000
	repo.carpools[10].QuotaSource = CarpoolQuotaSourceManual
	repo.carpools[10].ReserveRatio = 0.05
	repo.members[10][0].SharePercent = 50
	repo.members[10][1].SharePercent = 50

	syncCarpool(t, svc, repo, 10, time.Now())
	require.InDelta(t, 475, *repo.subs[501].limit, 1e-9, "1000 × (1 - 5%) × 50%")
	require.InDelta(t, 475, *repo.subs[502].limit, 1e-9)
	require.InDelta(t, 950, *admin.groupWeekly[1000], 1e-9, "分组兜底也扣掉预留")

	// manager 在调整额度里改预留
	reserve := 0.1
	view, err := svc.ManagerUpdateQuota(context.Background(), 10, 1, &UpdateCarpoolQuotaInput{ReserveRatio: &reserve})
	require.NoError(t, err)
	require.Equal(t, 0.1, view.ReserveRatio)
	require.InDelta(t, 900, view.DistributableWeekly, 1e-9)
	require.InDelta(t, 450, *repo.subs[501].limit, 1e-9)
	require.Equal(t, CarpoolQuotaSourceManual, repo.carpools[10].QuotaSource, "只改预留不影响额度来源")

	bad := 1.0
	_, err = svc.ManagerUpdateQuota(context.Background(), 10, 1, &UpdateCarpoolQuotaInput{ReserveRatio: &bad})
	require.ErrorIs(t, err, ErrCarpoolInvalidQuota)
	_, err = svc.ManagerUpdateQuota(context.Background(), 10, 2, &UpdateCarpoolQuotaInput{ReserveRatio: &reserve})
	require.ErrorIs(t, err, ErrCarpoolForbidden, "只读成员不能改预留")
}

func TestSetMemberShare(t *testing.T) {
	svc, repo, _, _ := newCarpoolTestService()
	repo.carpools[10].WeeklyQuotaUSD = 200
	repo.carpools[10].QuotaSource = CarpoolQuotaSourceManual
	ctx := context.Background()

	_, err := svc.SetMemberShare(ctx, 10, 2, 2, 50)
	require.ErrorIs(t, err, ErrCarpoolForbidden, "只读成员不能调比例")
	_, err = svc.SetMemberShare(ctx, 10, 1, 2, 61)
	require.ErrorIs(t, err, ErrCarpoolInvalidShare, "40 + 61 超过 100%")

	view, err := svc.SetMemberShare(ctx, 10, 1, 2, 60)
	require.NoError(t, err)
	require.Equal(t, 100.0, view.AllocatedPercent)
	require.InDelta(t, 120, *repo.subs[502].limit, 1e-9)

	_, err = svc.SetMemberShare(ctx, 10, 0, 2, 101)
	require.ErrorIs(t, err, ErrCarpoolInvalidShare)
}

// ---------- 周窗口绑定账号 ----------

func TestSyncLimits_AlignsMemberWindowsToAccount(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	now := time.Now()
	resetAt := now.Add(3 * 24 * time.Hour).Truncate(time.Second)
	accountStart := resetAt.Add(-7 * 24 * time.Hour)
	admin.setWindow(40, resetAt)

	stale := accountStart.Add(-2 * 24 * time.Hour) // 上一个账号窗口开的
	fresh := now.Add(-time.Hour)                   // 本窗口内首次使用才开窗
	repo.subs[501].windowStart, repo.subs[501].usage = &stale, 12
	repo.subs[502].windowStart, repo.subs[502].usage = &fresh, 5

	syncCarpool(t, svc, repo, 10, now)
	require.True(t, repo.subs[501].windowStart.Equal(accountStart))
	require.True(t, repo.subs[502].windowStart.Equal(accountStart))
	require.Zero(t, repo.subs[501].usage, "跨了账号窗口的用量清零")
	require.Equal(t, 5.0, repo.subs[502].usage, "本窗口内的用量保留")

	// 使用重置卡：账号重置时间提前到 7 天后，成员周用量一并清零
	repo.subs[502].usage = 8
	newReset := now.Add(7 * 24 * time.Hour).Truncate(time.Second)
	admin.setWindow(0, newReset)
	syncCarpool(t, svc, repo, 10, now.Add(time.Minute))
	require.True(t, repo.subs[502].windowStart.Equal(newReset.Add(-7*24*time.Hour)))
	require.Zero(t, repo.subs[502].usage)

	// 重复同步幂等
	repo.subs[502].usage = 3
	syncCarpool(t, svc, repo, 10, now.Add(2*time.Minute))
	require.Equal(t, 3.0, repo.subs[502].usage)
}

// ---------- 预估 ----------

func TestRefreshEstimate_ExcludesUsageBeforeCarpool(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	now := time.Now()
	resetAt := now.Add(3 * 24 * time.Hour)

	// 建车时账号已用 40%（可能不是经本站用的），不能算进样本
	admin.setWindow(40, resetAt)
	repo.accountCost = 2
	c := syncCarpool(t, svc, repo, 10, now)
	require.Zero(t, c.WeeklyQuotaUSD, "基线刚建立，还没有样本")
	require.Equal(t, 40.0, c.BaselineUsedPercent)
	require.Nil(t, admin.groupWeekly[1000], "尚未预估时分组不限额")
	require.Nil(t, repo.subs[501].limit, "尚未预估时车位不限额")

	admin.setWindow(44, resetAt)
	c = syncCarpool(t, svc, repo, 10, now.Add(time.Hour))
	require.Zero(t, c.EstimatedWeeklyUSD, "增量不足 5% 不预估")

	// 上涨 10% 花了 $30 → 周额度 $300；alice 40% = $120
	admin.setWindow(50, resetAt)
	repo.accountCost = 30
	c = syncCarpool(t, svc, repo, 10, now.Add(2*time.Hour))
	require.InDelta(t, 300, c.WeeklyQuotaUSD, 1e-9)
	require.Equal(t, now, repo.costSince)
	require.InDelta(t, 120, *repo.subs[501].limit, 1e-9)
}

func TestRefreshEstimate_ConvergesWithUsage(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	now := time.Now()
	resetAt := now.Add(6 * 24 * time.Hour)
	admin.setWindow(0, resetAt)
	syncCarpool(t, svc, repo, 10, now)

	admin.setWindow(10, resetAt)
	repo.accountCost = 100
	c := syncCarpool(t, svc, repo, 10, now.Add(time.Hour))
	require.InDelta(t, 1000, c.WeeklyQuotaUSD, 1e-9)

	admin.setWindow(50, resetAt)
	repo.accountCost = 678
	c = syncCarpool(t, svc, repo, 10, now.Add(2*time.Hour))
	require.InDelta(t, 1356, c.WeeklyQuotaUSD, 1e-9)

	// 变化不到 1% 不改额度，也不刷新分组
	updates := len(admin.groupInputs)
	repo.accountCost = 679
	c = syncCarpool(t, svc, repo, 10, now.Add(3*time.Hour))
	require.InDelta(t, 1358, c.EstimatedWeeklyUSD, 1e-9)
	require.InDelta(t, 1356, c.WeeklyQuotaUSD, 1e-9)
	require.Len(t, admin.groupInputs, updates)
}

func TestRefreshEstimate_NewWindowKeepsPreviousEstimate(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	now := time.Now()
	resetAt := now.Add(time.Hour)
	admin.setWindow(10, resetAt)
	syncCarpool(t, svc, repo, 10, now)
	admin.setWindow(60, resetAt)
	repo.accountCost = 500
	c := syncCarpool(t, svc, repo, 10, now.Add(time.Minute))
	require.InDelta(t, 1000, c.WeeklyQuotaUSD, 1e-9)

	// 重置时间抖动不算新窗口
	admin.setWindow(60, resetAt.Add(10*time.Minute))
	c = syncCarpool(t, svc, repo, 10, now.Add(2*time.Minute))
	require.Equal(t, 10.0, c.BaselineUsedPercent)

	nextReset := resetAt.Add(7 * 24 * time.Hour)
	admin.setWindow(1, nextReset)
	c = syncCarpool(t, svc, repo, 10, resetAt.Add(time.Hour))
	require.InDelta(t, 1000, c.WeeklyQuotaUSD, 1e-9, "新窗口样本不足时沿用上次预估")
	require.Zero(t, c.BaselineUsedPercent)
	require.Equal(t, resetAt.Truncate(time.Second).UTC(), c.BaselineAt.Truncate(time.Second).UTC())
}

func TestRefreshEstimate_ManualQuotaNotOverwritten(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	repo.carpools[10].QuotaSource = CarpoolQuotaSourceManual
	repo.carpools[10].WeeklyQuotaUSD = 800
	now := time.Now()
	resetAt := now.Add(24 * time.Hour)
	admin.setWindow(0, resetAt)
	syncCarpool(t, svc, repo, 10, now)
	admin.setWindow(50, resetAt)
	repo.accountCost = 100

	c := syncCarpool(t, svc, repo, 10, now.Add(time.Hour))
	require.InDelta(t, 200, c.EstimatedWeeklyUSD, 1e-9, "手动模式也继续预估，方便对比")
	require.InDelta(t, 800, c.WeeklyQuotaUSD, 1e-9)
	require.InDelta(t, 320, *repo.subs[501].limit, 1e-9, "40% × 800")
}

func TestSync_MissingSnapshotKeepsWindows(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	repo.carpools[10].WeeklyQuotaUSD = 400
	start := time.Now().Add(-48 * time.Hour)
	repo.subs[501].windowStart, repo.subs[501].usage = &start, 7

	c := syncCarpool(t, svc, repo, 10, time.Now())
	require.InDelta(t, 400, c.WeeklyQuotaUSD, 1e-9)
	require.True(t, repo.subs[501].windowStart.Equal(start), "没有账号快照时不动订阅窗口")
	require.Equal(t, 7.0, repo.subs[501].usage)

	admin.setWindow(90, time.Now().Add(-time.Minute))
	syncCarpool(t, svc, repo, 10, time.Now())
	require.Equal(t, 7.0, repo.subs[501].usage, "快照窗口已过期也不动")
}

func TestSyncGroupLimit_OnlyTouchesWeeklyLimit(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	weekly := 600.0
	_, err := svc.ManagerUpdateQuota(context.Background(), 10, 1, &UpdateCarpoolQuotaInput{WeeklyQuotaUSD: &weekly})
	require.NoError(t, err)

	require.Len(t, admin.groupInputs, 1)
	// 只设周限额，其余字段保持零值 = 不修改，模型定价等分组设置不受影响
	require.Equal(t, &UpdateGroupInput{WeeklyLimitUSD: admin.groupInputs[0].WeeklyLimitUSD}, admin.groupInputs[0])

	tampered := 1.0
	admin.groupWeekly[1000] = &tampered
	syncCarpool(t, svc, repo, 10, time.Now())
	require.InDelta(t, 600, *admin.groupWeekly[1000], 1e-9, "分组页改的周限额被修正回来")
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
	repo.carpools[10].QuotaSource = CarpoolQuotaSourceManual
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
	require.InDelta(t, 160, *detail.Members[0].WeeklyLimitUSD, 1e-9, "40% × 400")
	require.Equal(t, 40.0, detail.Members[0].SharePercent)
	require.Equal(t, resetAt.Unix(), detail.Members[0].WeeklyResetsAt.Unix(), "成员重置时间跟随账号窗口")
	require.Equal(t, 70.0, detail.Carpool.AllocatedPercent)

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
	require.Equal(t, 600.0, view.WeeklyQuotaUSD)
	require.Equal(t, CarpoolQuotaSourceManual, repo.carpools[10].QuotaSource)

	on := false
	require.ErrorIs(t, svc.AccountAction(ctx, 10, 2, CarpoolAccountActionSetSchedulable, &on), ErrCarpoolForbidden)
	require.NoError(t, svc.AccountAction(ctx, 10, 1, CarpoolAccountActionSetSchedulable, &on))
	require.False(t, *admin.schedulable)
}

func TestResetCards_PermissionsAndSupport(t *testing.T) {
	svc, _, admin, _ := newCarpoolTestService()
	ctx := context.Background()

	_, err := svc.GetResetCards(ctx, 20, 1)
	require.ErrorIs(t, err, ErrCarpoolNotFound, "看不到别的车的重置卡")

	// API Key 账号没有重置卡
	cards, err := svc.GetResetCards(ctx, 10, 2)
	require.NoError(t, err)
	require.False(t, cards.Supported)
	require.Equal(t, CarpoolResetCardThresholdPercent, cards.ThresholdPercent)

	_, err = svc.UseResetCard(ctx, 10, 2)
	require.ErrorIs(t, err, ErrCarpoolForbidden, "只读成员不能用重置卡")
	_, err = svc.UseResetCard(ctx, 10, 1)
	require.ErrorIs(t, err, ErrCarpoolResetUnsupported)

	admin.account.Type = AccountTypeOAuth
	require.True(t, carpoolSupportsResetCard(admin.account))
}

func TestRefreshEstimate_InitialQuotaKeptUntilFirstEstimate(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	repo.carpools[10].WeeklyQuotaUSD = 2000 // 建车时给的初始额度，来源仍是 auto
	repo.members[10][0].SharePercent = 25
	now := time.Now()
	resetAt := now.Add(5 * 24 * time.Hour)
	admin.setWindow(2, resetAt)

	c := syncCarpool(t, svc, repo, 10, now)
	require.InDelta(t, 2000, c.WeeklyQuotaUSD, 1e-9, "没有预估时不能把初始额度清零")
	require.InDelta(t, 500, *repo.subs[501].limit, 1e-9, "25% × 2000，和接管前分组限额一致")

	admin.setWindow(12, resetAt)
	repo.accountCost = 300
	c = syncCarpool(t, svc, repo, 10, now.Add(time.Hour))
	require.InDelta(t, 3000, c.WeeklyQuotaUSD, 1e-9, "首个预估生成后替换初始额度")
}

func TestRefreshEstimate_CountCurrentWindowStartsAtWindowStart(t *testing.T) {
	svc, repo, admin, _ := newCarpoolTestService()
	now := time.Now()
	resetAt := now.Add(4 * 24 * time.Hour).Truncate(time.Second)
	admin.setWindow(30, resetAt)
	repo.accountCost = 600

	c, err := repo.GetByID(context.Background(), 10)
	require.NoError(t, err)
	c.countCurrentWindow = true
	require.NoError(t, svc.syncLocked(context.Background(), c, now))

	stored := repo.carpools[10]
	require.Zero(t, stored.BaselineUsedPercent)
	require.True(t, stored.BaselineAt.Equal(resetAt.Add(-7*24*time.Hour)), "样本从窗口起点算起")
	require.InDelta(t, 2000, stored.WeeklyQuotaUSD, 1e-9, "立即用本窗口已有用量预估：600 / 30%")
}
