package repository

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type carpoolRepository struct {
	db *sql.DB
}

func NewCarpoolRepository(db *sql.DB) service.CarpoolRepository {
	return &carpoolRepository{db: db}
}

const carpoolSelectColumns = `
	c.id, c.name, c.account_id, c.group_id,
	c.weekly_quota_usd, c.quota_source, c.seats, c.validity_days, c.notes, c.status,
	c.created_by, c.created_at, c.updated_at,
	c.estimated_weekly_usd, c.window_reset_at, c.baseline_at, c.baseline_used_percent,
	(SELECT COUNT(*) FROM carpool_members m WHERE m.carpool_id = c.id) AS member_count,
	(SELECT COALESCE(SUM(m.share_percent), 0) FROM carpool_members m WHERE m.carpool_id = c.id) AS allocated_percent`

func scanCarpool(row rowScanner) (*service.Carpool, error) {
	var (
		c             service.Carpool
		createdBy     sql.NullInt64
		windowResetAt sql.NullTime
		baselineAt    sql.NullTime
	)
	if err := row.Scan(
		&c.ID, &c.Name, &c.AccountID, &c.GroupID,
		&c.WeeklyQuotaUSD, &c.QuotaSource, &c.Seats, &c.ValidityDays, &c.Notes, &c.Status,
		&createdBy, &c.CreatedAt, &c.UpdatedAt,
		&c.EstimatedWeeklyUSD, &windowResetAt, &baselineAt, &c.BaselineUsedPercent, &c.MemberCount, &c.AllocatedPercent,
	); err != nil {
		return nil, err
	}
	if createdBy.Valid {
		v := createdBy.Int64
		c.CreatedBy = &v
	}
	if windowResetAt.Valid {
		t := windowResetAt.Time
		c.WindowResetAt = &t
	}
	if baselineAt.Valid {
		t := baselineAt.Time
		c.BaselineAt = &t
	}
	return &c, nil
}

func (r *carpoolRepository) queryCarpools(ctx context.Context, query string, args ...any) ([]service.Carpool, error) {
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.Carpool, 0)
	for rows.Next() {
		c, err := scanCarpool(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (r *carpoolRepository) getOne(ctx context.Context, where string, arg any) (*service.Carpool, error) {
	row := r.db.QueryRowContext(ctx,
		`SELECT `+carpoolSelectColumns+` FROM carpools c WHERE c.deleted_at IS NULL AND `+where, arg)
	c, err := scanCarpool(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCarpoolNotFound
	}
	return c, err
}

func (r *carpoolRepository) Create(ctx context.Context, c *service.Carpool) error {
	return r.db.QueryRowContext(ctx, `
		INSERT INTO carpools (name, account_id, group_id,
			weekly_quota_usd, quota_source, seats, validity_days, notes, status, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, created_at, updated_at`,
		c.Name, c.AccountID, c.GroupID,
		c.WeeklyQuotaUSD, c.QuotaSource, c.Seats, c.ValidityDays, c.Notes, c.Status, c.CreatedBy,
	).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
}

func (r *carpoolRepository) GetByID(ctx context.Context, id int64) (*service.Carpool, error) {
	return r.getOne(ctx, "c.id = $1", id)
}

func (r *carpoolRepository) GetByAccountID(ctx context.Context, accountID int64) (*service.Carpool, error) {
	return r.getOne(ctx, "c.account_id = $1", accountID)
}

func (r *carpoolRepository) List(ctx context.Context) ([]service.Carpool, error) {
	return r.queryCarpools(ctx,
		`SELECT `+carpoolSelectColumns+` FROM carpools c WHERE c.deleted_at IS NULL ORDER BY c.id DESC`)
}

func (r *carpoolRepository) ListByUser(ctx context.Context, userID int64) ([]service.Carpool, error) {
	return r.queryCarpools(ctx, `
		SELECT `+carpoolSelectColumns+` FROM carpools c
		WHERE c.deleted_at IS NULL
		  AND (EXISTS (SELECT 1 FROM carpool_members m WHERE m.carpool_id = c.id AND m.user_id = $1)
		       -- 订阅页刚分配、还没同步成车位的拼车分组订阅
		       OR EXISTS (SELECT 1 FROM user_subscriptions s
		                  WHERE s.group_id = c.group_id AND s.user_id = $1 AND s.deleted_at IS NULL))
		ORDER BY c.id DESC`, userID)
}

func (r *carpoolRepository) Update(ctx context.Context, c *service.Carpool) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE carpools SET name = $2,
			weekly_quota_usd = $3, quota_source = $4, seats = $5, validity_days = $6,
			notes = $7, status = $8, updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL`,
		c.ID, c.Name,
		c.WeeklyQuotaUSD, c.QuotaSource, c.Seats, c.ValidityDays, c.Notes, c.Status)
	if err != nil {
		return err
	}
	return requireAffected(res, service.ErrCarpoolNotFound)
}

func (r *carpoolRepository) SaveEstimateState(ctx context.Context, id int64, state *service.CarpoolEstimateState, expectedWindowResetAt *time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE carpools SET
			estimated_weekly_usd = $2::numeric,
			window_reset_at = $3,
			baseline_at = $4,
			baseline_used_percent = $5,
			weekly_quota_usd = CASE WHEN $6 AND quota_source = 'auto' THEN $2::numeric ELSE weekly_quota_usd END,
			updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL AND window_reset_at IS NOT DISTINCT FROM $7::timestamptz`,
		id, state.EstimatedWeeklyUSD, state.WindowResetAt, state.BaselineAt, state.BaselineUsedPercent,
		state.ApplyToQuota, expectedWindowResetAt)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (r *carpoolRepository) SoftDelete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE carpools SET deleted_at = NOW(), updated_at = NOW() WHERE id = $1 AND deleted_at IS NULL`, id)
	if err != nil {
		return err
	}
	if err := requireAffected(res, service.ErrCarpoolNotFound); err != nil {
		return err
	}
	_, err = r.db.ExecContext(ctx, `DELETE FROM carpool_members WHERE carpool_id = $1`, id)
	return err
}

const carpoolMemberSelect = `
	SELECT m.id, m.carpool_id, m.user_id, m.subscription_id, m.role, m.share_percent, m.created_at,
		COALESCE(u.email, ''), COALESCE(u.username, '')
	FROM carpool_members m
	LEFT JOIN users u ON u.id = m.user_id`

func scanCarpoolMember(row rowScanner) (*service.CarpoolMember, error) {
	var (
		m     service.CarpoolMember
		subID sql.NullInt64
	)
	if err := row.Scan(&m.ID, &m.CarpoolID, &m.UserID, &subID, &m.Role, &m.SharePercent, &m.CreatedAt, &m.Email, &m.Username); err != nil {
		return nil, err
	}
	if subID.Valid {
		v := subID.Int64
		m.SubscriptionID = &v
	}
	return &m, nil
}

func (r *carpoolRepository) ListMembers(ctx context.Context, carpoolID int64) ([]service.CarpoolMember, error) {
	rows, err := r.db.QueryContext(ctx, carpoolMemberSelect+` WHERE m.carpool_id = $1 ORDER BY m.id`, carpoolID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]service.CarpoolMember, 0)
	for rows.Next() {
		m, err := scanCarpoolMember(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *m)
	}
	return out, rows.Err()
}

func (r *carpoolRepository) GetMember(ctx context.Context, carpoolID, userID int64) (*service.CarpoolMember, error) {
	row := r.db.QueryRowContext(ctx, carpoolMemberSelect+` WHERE m.carpool_id = $1 AND m.user_id = $2`, carpoolID, userID)
	m, err := scanCarpoolMember(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrCarpoolMemberNotFound
	}
	return m, err
}

func (r *carpoolRepository) AddMember(ctx context.Context, m *service.CarpoolMember) error {
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO carpool_members (carpool_id, user_id, subscription_id, role, share_percent)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`,
		m.CarpoolID, m.UserID, m.SubscriptionID, m.Role, m.SharePercent,
	).Scan(&m.ID, &m.CreatedAt)
	if isUniqueConstraintViolation(err) {
		return service.ErrCarpoolMemberExists
	}
	return err
}

func (r *carpoolRepository) UpdateMemberRole(ctx context.Context, carpoolID, userID int64, role string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE carpool_members SET role = $3 WHERE carpool_id = $1 AND user_id = $2`, carpoolID, userID, role)
	if err != nil {
		return err
	}
	return requireAffected(res, service.ErrCarpoolMemberNotFound)
}

func (r *carpoolRepository) UpdateMemberShare(ctx context.Context, carpoolID, userID int64, sharePercent float64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE carpool_members SET share_percent = $3 WHERE carpool_id = $1 AND user_id = $2`, carpoolID, userID, sharePercent)
	if err != nil {
		return err
	}
	return requireAffected(res, service.ErrCarpoolMemberNotFound)
}

func (r *carpoolRepository) ListLiveGroupSubscriptions(ctx context.Context, groupID int64) (map[int64]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT user_id, id FROM user_subscriptions WHERE group_id = $1 AND deleted_at IS NULL`, groupID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]int64)
	for rows.Next() {
		var userID, subID int64
		if err := rows.Scan(&userID, &subID); err != nil {
			return nil, err
		}
		out[userID] = subID
	}
	return out, rows.Err()
}

func (r *carpoolRepository) SetSubscriptionWeeklyLimit(ctx context.Context, subscriptionID int64, limit *float64) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE user_subscriptions SET weekly_limit_usd = $2::numeric, updated_at = NOW()
		WHERE id = $1 AND weekly_limit_usd IS DISTINCT FROM $2::numeric`, subscriptionID, limit)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// AlignSubscriptionWeeklyWindow 窗口起点早于账号窗口起点（再留 1 小时余量）的订阅属于上一个账号窗口，
// 对齐时清零周用量；晚于账号窗口起点的（窗口内新加入、首次使用才开窗）只挪起点不清零。
func (r *carpoolRepository) AlignSubscriptionWeeklyWindow(ctx context.Context, subscriptionID int64, start time.Time) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		UPDATE user_subscriptions SET
			weekly_usage_usd = CASE
				WHEN weekly_window_start IS NULL OR weekly_window_start < $2::timestamptz - interval '1 hour' THEN 0
				ELSE weekly_usage_usd END,
			weekly_window_start = $2::timestamptz,
			updated_at = NOW()
		WHERE id = $1
		  AND (weekly_window_start IS NULL
		       OR abs(extract(epoch FROM weekly_window_start - $2::timestamptz)) > 3600)`, subscriptionID, start)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (r *carpoolRepository) SetMemberSubscription(ctx context.Context, carpoolID, userID, subscriptionID int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE carpool_members SET subscription_id = $3 WHERE carpool_id = $1 AND user_id = $2`,
		carpoolID, userID, subscriptionID)
	if err != nil {
		return err
	}
	return requireAffected(res, service.ErrCarpoolMemberNotFound)
}

func (r *carpoolRepository) RemoveMember(ctx context.Context, carpoolID, userID int64) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM carpool_members WHERE carpool_id = $1 AND user_id = $2`, carpoolID, userID)
	if err != nil {
		return err
	}
	return requireAffected(res, service.ErrCarpoolMemberNotFound)
}

func (r *carpoolRepository) GetGroupUsageByUser(ctx context.Context, groupID int64, since time.Time) (map[int64]*service.CarpoolMemberUsage, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id,
			COUNT(*),
			COALESCE(SUM(input_tokens + output_tokens + cache_creation_tokens + cache_read_tokens), 0),
			COALESCE(SUM(total_cost), 0),
			COALESCE(SUM(actual_cost), 0),
			MAX(created_at)
		FROM usage_logs
		WHERE group_id = $1 AND created_at >= $2
		GROUP BY user_id`, groupID, since)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make(map[int64]*service.CarpoolMemberUsage)
	for rows.Next() {
		var (
			u    service.CarpoolMemberUsage
			last sql.NullTime
		)
		if err := rows.Scan(&u.UserID, &u.Requests, &u.Tokens, &u.StandardCost, &u.ActualCost, &last); err != nil {
			return nil, err
		}
		if last.Valid {
			t := last.Time
			u.LastUsedAt = &t
		}
		out[u.UserID] = &u
	}
	return out, rows.Err()
}

func (r *carpoolRepository) GetAccountActualCostSince(ctx context.Context, accountID int64, since time.Time) (float64, error) {
	var cost float64
	err := r.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(actual_cost), 0) FROM usage_logs WHERE account_id = $1 AND created_at >= $2`,
		accountID, since).Scan(&cost)
	return cost, err
}

func requireAffected(res sql.Result, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return notFound
	}
	return nil
}
