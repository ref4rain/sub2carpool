package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCarpoolResetCards_ConsumeEarliestUnexpiredCredit(t *testing.T) {
	now := time.Now()
	earlier := now.Add(24 * time.Hour).Format(time.RFC3339)
	later := now.Add(48 * time.Hour).Format(time.RFC3339)
	expired := now.Add(-time.Hour).Format(time.RFC3339)
	credit := func(id, expiry string) string {
		return fmt.Sprintf(`{"id":%q,"status":"available","reset_type":"codex_rate_limits","expires_at":%q}`, id, expiry)
	}
	details := func(count int, records ...string) string {
		return fmt.Sprintf(`{"available_count":%d,"credits":[%s]}`, count, strings.Join(records, ","))
	}
	tests := []struct {
		name         string
		detailBody   string
		detailFails  bool
		belowLimit   bool
		consumeFails bool
		wantCredit   string
		wantReason   string
		wantCanUse   bool
	}{
		{
			name:       "unordered list selects earliest expiry",
			detailBody: details(2, credit("sensitive-later", later), credit("sensitive-earlier", earlier)),
			wantCredit: "sensitive-earlier", wantCanUse: true,
		},
		{
			name: "expiration order compares instants across timezones",
			detailBody: details(2, credit("sensitive-later", later),
				credit("sensitive-earlier", now.Add(24*time.Hour).In(time.FixedZone("UTC+8", 8*3600)).Format(time.RFC3339))),
			wantCredit: "sensitive-earlier", wantCanUse: true,
		},
		{
			name:       "expired credit skipped",
			detailBody: details(2, credit("sensitive-expired", expired), credit("sensitive-later", later)),
			wantCredit: "sensitive-later", wantCanUse: true,
		},
		{
			name:       "available count already excludes expired credit",
			detailBody: details(1, credit("sensitive-expired", expired), credit("sensitive-later", later)),
			wantCredit: "sensitive-later", wantCanUse: true,
		},
		{
			name: "redeemed and other grant types ignored",
			detailBody: details(1,
				fmt.Sprintf(`{"id":"sensitive-redeemed","status":"redeemed","expires_at":%q}`, earlier),
				fmt.Sprintf(`{"id":"sensitive-other","reset_type":"other","expires_at":%q}`, earlier),
				credit("sensitive-later", later)),
			wantCredit: "sensitive-later", wantCanUse: true,
		},
		{
			name:       "no available credits",
			detailBody: details(0), wantReason: "OPENAI_AUTO_RESET_NO_CREDIT",
		},
		{
			name:       "all credits expired",
			detailBody: details(1, credit("sensitive-expired", expired)), wantReason: "OPENAI_AUTO_RESET_NO_CREDIT",
		},
		{
			name:       "incomplete list refuses consumption",
			detailBody: details(2, credit("sensitive-later", later)), wantReason: "OPENAI_AUTO_RESET_CREDIT_DETAILS_INCOMPLETE",
		},
		{
			name:       "missing earliest id does not switch to later credit",
			detailBody: details(2, credit("sensitive-later", later), credit("", earlier)),
			wantReason: "OPENAI_AUTO_RESET_CREDIT_ID_MISSING",
		},
		{
			name:       "missing expiry refuses consumption",
			detailBody: details(2, credit("sensitive-later", later), credit("sensitive-earlier", "")),
			wantReason: "OPENAI_AUTO_RESET_CREDIT_DETAILS_INCOMPLETE",
		},
		{
			name:       "invalid expiry refuses consumption",
			detailBody: details(2, credit("sensitive-later", later), credit("sensitive-earlier", "invalid")),
			wantReason: "OPENAI_AUTO_RESET_CREDIT_EXPIRY_INVALID",
		},
		{
			name:        "detail endpoint failure does not use untargeted reset",
			detailFails: true, wantReason: "OPENAI_AUTO_RESET_CREDIT_DETAILS_INCOMPLETE",
		},
		{
			name:       "weekly usage threshold still enforced",
			detailBody: details(1, credit("sensitive-earlier", earlier)), belowLimit: true,
			wantReason: "CARPOOL_RESET_NOT_ALLOWED",
		},
		{
			name:         "targeted failure does not consume a different credit",
			detailBody:   details(2, credit("sensitive-later", later), credit("sensitive-earlier", earlier)),
			consumeFails: true, wantCredit: "sensitive-earlier", wantCanUse: true,
			wantReason: "OPENAI_QUOTA_RESET_UPSTREAM_ERROR",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, _, admin, _ := newCarpoolTestService()
			account := admin.account
			account.Type = AccountTypeOAuth
			account.Status = StatusActive
			account.Credentials = map[string]any{"chatgpt_account_id": "test-account"}
			accounts := &stubQuotaAccountRepo{accounts: map[int64]*Account{account.ID: account}}
			cache := &stubQuotaTokenCache{tokens: map[string]string{OpenAITokenCacheKey(account): "fake-token"}}
			var consumed atomic.Bool
			requests := make(chan map[string]string, 4)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("content-type", "application/json")
				switch r.URL.Path {
				case "/backend-api/wham/usage":
					used := 85
					if tc.belowLimit {
						used = 79
					}
					if consumed.Load() {
						used = 0
					}
					_, _ = fmt.Fprintf(w, `{"rate_limit":{"secondary_window":{"used_percent":%d,"limit_window_seconds":604800}},"rate_limit_reset_credits":{"available_count":2}}`, used)
				case "/backend-api/wham/rate-limit-reset-credits":
					if tc.detailFails {
						w.WriteHeader(http.StatusBadGateway)
						_, _ = w.Write([]byte(`{}`))
					} else {
						_, _ = w.Write([]byte(tc.detailBody))
					}
				case "/backend-api/wham/rate-limit-reset-credits/consume":
					var body map[string]string
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					requests <- body
					if tc.consumeFails {
						w.WriteHeader(http.StatusConflict)
						_, _ = w.Write([]byte(`{"error":"credit unavailable"}`))
					} else {
						consumed.Store(true)
						_, _ = w.Write([]byte(`{"code":"ok","windows_reset":1}`))
					}
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(srv.Close)
			svc.quotaService = NewOpenAIQuotaService(accounts, nil, NewOpenAITokenProvider(accounts, cache, nil), newQuotaRedirectingFactory(srv), nil)
			svc.rateLimitService = &RateLimitService{accountRepo: accounts}

			cards, err := svc.GetResetCards(context.Background(), 10, 1)
			require.NoError(t, err)
			require.Equal(t, tc.wantCanUse, cards.CanUse)
			publicCards, err := json.Marshal(cards)
			require.NoError(t, err)
			require.NotContains(t, string(publicCards), "sensitive-", "卡 ID 不得进入查询响应")

			result, err := svc.UseResetCard(context.Background(), 10, 1)
			if tc.wantReason != "" {
				require.Error(t, err)
				require.Equal(t, tc.wantReason, infraerrors.Reason(err))
				require.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.Equal(t, "ok", result.Result.Code)
				require.Empty(t, result.WarningCode)
			}
			if tc.wantCredit == "" {
				require.Empty(t, requests, "选卡失败或用量不足时不得发起消费")
			} else {
				require.Len(t, requests, 1, "只允许消费指定的一张卡，失败不得换卡重试")
				body := <-requests
				require.Equal(t, tc.wantCredit, body["credit_id"])
				_, err := uuid.Parse(body["redeem_request_id"])
				require.NoError(t, err)
			}
		})
	}
}
