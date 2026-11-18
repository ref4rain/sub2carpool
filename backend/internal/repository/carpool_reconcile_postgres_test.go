package repository

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// TEMP tables shadow business tables on this connection; no production table is modified.
func TestCarpoolWeeklyReconcile_Postgres(t *testing.T) {
	dsn := os.Getenv("CARPOOL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CARPOOL_TEST_DSN to run PostgreSQL reconciliation regression")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	ctx := context.Background()
	_, err = db.ExecContext(ctx, `
		CREATE TEMP TABLE user_subscriptions (
			id bigint PRIMARY KEY, user_id bigint, group_id bigint,
			weekly_window_start timestamptz, weekly_usage_usd numeric(20,8),
			updated_at timestamptz, deleted_at timestamptz);
		CREATE TEMP TABLE usage_logs (
			subscription_id bigint, user_id bigint, group_id bigint, billing_type smallint,
			actual_cost numeric(20,10), created_at timestamptz);`)
	require.NoError(t, err)
	repo := NewCarpoolRepository(db)
	start := time.Now().UTC().Truncate(time.Second).Add(-72 * time.Hour)
	for _, tc := range []struct {
		name              string
		offset            time.Duration
		current, expected float64
	}{
		{"later start restores missing history", 24 * time.Hour, 534.2712, 800.816},
		{"earlier start retains target window spend", -7 * 24 * time.Hour, 999, 800.816},
		{"aligned undercount is repaired", 0, 534.2712, 800.816},
		{"aligned pending logs never reduce billed counter", 0, 801.5, 801.5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := db.ExecContext(ctx, `TRUNCATE pg_temp.user_subscriptions, pg_temp.usage_logs`)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `INSERT INTO user_subscriptions (id,user_id,group_id,weekly_window_start,weekly_usage_usd) VALUES (2,3,4,$1,$2)`, start.Add(tc.offset), tc.current)
			require.NoError(t, err)
			_, err = db.ExecContext(ctx, `INSERT INTO usage_logs VALUES
				(2,3,4,1,266.544800004,$1), (2,3,4,1,534.2712,$2),
				(99,3,4,1,50,$1), (2,99,4,1,50,$1), (2,3,99,1,50,$1),
				(2,3,4,0,50,$1), (2,3,4,1,50,$3), (2,3,4,1,50,$4),
				(2,3,4,1,50,$5)`, start, start.Add(24*time.Hour), start.Add(-time.Second), start.Add(7*24*time.Hour), time.Now().Add(time.Hour))
			require.NoError(t, err)
			changed, err := repo.AlignSubscriptionWeeklyWindow(ctx, 2, start)
			require.NoError(t, err)
			require.Equal(t, tc.offset != 0 || tc.current < tc.expected, changed)
			var usage float64
			var actualStart time.Time
			require.NoError(t, db.QueryRowContext(ctx, `SELECT weekly_usage_usd, weekly_window_start FROM user_subscriptions WHERE id=2`).Scan(&usage, &actualStart))
			require.InDelta(t, tc.expected, usage, 1e-8)
			require.True(t, actualStart.Equal(start))
			changed, err = repo.AlignSubscriptionWeeklyWindow(ctx, 2, start)
			require.NoError(t, err)
			require.False(t, changed, "repeated reconciliation is idempotent")
			// An asynchronous log arriving after reconciliation is topped up next time.
			_, err = db.ExecContext(ctx, `INSERT INTO usage_logs VALUES (2,3,4,1,2,$1)`, start.Add(time.Hour))
			require.NoError(t, err)
			changed, err = repo.AlignSubscriptionWeeklyWindow(ctx, 2, start)
			require.NoError(t, err)
			require.True(t, changed)
			require.NoError(t, db.QueryRowContext(ctx, `SELECT weekly_usage_usd FROM user_subscriptions WHERE id=2`).Scan(&usage))
			require.InDelta(t, 802.816, usage, 1e-8)
			// A genuine new account window with no requests starts at zero.
			changed, err = repo.AlignSubscriptionWeeklyWindow(ctx, 2, start.Add(14*24*time.Hour))
			require.NoError(t, err)
			require.True(t, changed)
			require.NoError(t, db.QueryRowContext(ctx, `SELECT weekly_usage_usd FROM user_subscriptions WHERE id=2`).Scan(&usage))
			require.Zero(t, usage)
		})
	}
	t.Run("uninitialized window", func(t *testing.T) {
		_, err := db.ExecContext(ctx, `UPDATE user_subscriptions SET weekly_window_start=NULL,weekly_usage_usd=999`)
		require.NoError(t, err)
		changed, err := repo.AlignSubscriptionWeeklyWindow(ctx, 2, start)
		require.NoError(t, err)
		require.True(t, changed)
		var usage float64
		require.NoError(t, db.QueryRowContext(ctx, `SELECT weekly_usage_usd FROM user_subscriptions WHERE id=2`).Scan(&usage))
		require.InDelta(t, 802.816, usage, 1e-8)
	})
	_, err = repo.AlignSubscriptionWeeklyWindow(ctx, 999, start)
	require.ErrorIs(t, err, service.ErrSubscriptionNotFound)
}
