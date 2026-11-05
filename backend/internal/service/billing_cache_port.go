package service

import (
	"time"
)

// SubscriptionCacheData represents cached subscription data
type SubscriptionCacheData struct {
	Status       string
	ExpiresAt    time.Time
	DailyUsage   float64
	WeeklyUsage  float64
	MonthlyUsage float64
	// WeeklyLimit 订阅级周限额覆盖，nil 表示沿用分组
	WeeklyLimit *float64
	Version     int64
}
