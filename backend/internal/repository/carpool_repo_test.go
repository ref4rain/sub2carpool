package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestCarpoolGroupUsage_CalendarDayBounds(t *testing.T) {
	// UTC 日期仍是 16 日，但配置为 UTC+8 时已经是 17 日，不能用 UTC 零点。
	loc, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)
	originalZone := timezone.Name()
	require.NoError(t, timezone.Init(loc.String()))
	t.Cleanup(func() { require.NoError(t, timezone.Init(originalZone)) })
	now := time.Date(2026, 11, 16, 17, 30, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)
	midnight := time.Date(2026, 11, 17, 0, 0, 0, 0, loc)
	var query string
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherFunc(func(_, actual string) error {
		query = strings.Join(strings.Fields(actual), " ")
		return nil
	})))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectQuery("carpool usage").WithArgs(int64(5), since, now, midnight).
		WillReturnRows(sqlmock.NewRows([]string{"user_id", "requests", "tokens", "standard_cost", "actual_cost", "daily_actual_cost", "last_used_at"}).
			AddRow(8, 3, 400, 100, 50, 12.5, now.Add(-time.Minute)).
			AddRow(9, 2, 200, 80, 40, 0, now.Add(-24*time.Hour)))
	usage, err := NewCarpoolRepository(db).GetGroupUsageByUser(context.Background(), 5, since, now)
	require.NoError(t, err)
	require.Len(t, usage, 2)
	require.Equal(t, 12.5, usage[8].DailyActualCost)
	require.Equal(t, 50.0, usage[8].ActualCost)
	require.Zero(t, usage[9].DailyActualCost)
	require.NotNil(t, usage[9].LastUsedAt)
	require.Contains(t, query, "SUM(actual_cost) FILTER (WHERE created_at >= $4)")
	require.Contains(t, query, "WHERE group_id = $1 AND created_at >= $2 AND created_at < $3")
	require.NoError(t, mock.ExpectationsWereMet())
}
