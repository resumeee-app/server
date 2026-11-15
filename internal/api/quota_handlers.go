package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/resumeee-app/resumeee-api/internal/config"
	"github.com/resumeee-app/resumeee-core"
)

// QuotaResponse describes the tenant's current quota status, usage, limits, and reset window timestamps.
type QuotaResponse struct {
	core.TenantQuota
	DailyRequestsRemaining int       `json:"daily_requests_remaining"`
	MonthlyTokensRemaining int       `json:"monthly_tokens_remaining"`
	DailyResetAt           time.Time `json:"daily_reset_at"`
	MonthlyResetAt         time.Time `json:"monthly_reset_at"`
}

// registerQuotaRoutes binds tenant quota status endpoints.
func registerQuotaRoutes(mux *http.ServeMux, cfg RouterConfig) {
	h := &quotaHandler{cfg: cfg}
	mux.HandleFunc("GET /api/v1/quota", h.getQuota)
	mux.HandleFunc("GET /quota", h.getQuota)
}

// quotaHandler serves tenant rate-limit and token quota endpoints.
type quotaHandler struct {
	cfg RouterConfig
}

// getQuota returns the current rate limit and token quota status for the authenticated tenant.
func (h *quotaHandler) getQuota(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := requestUserID(r, h.cfg.DefaultUserID)

	dailyLimit := h.cfg.DailyRequestLimit
	if dailyLimit <= 0 {
		dailyLimit = config.DefaultLLMDailyRequestLimit
	}
	monthlyLimit := h.cfg.MonthlyTokenLimit
	if monthlyLimit <= 0 {
		monthlyLimit = config.DefaultLLMMonthlyTokenLimit
	}

	var quota *core.TenantQuota
	if h.cfg.QuotaStore != nil {
		q, err := h.cfg.QuotaStore.Quota(ctx, userID)
		if err != nil && !errors.Is(err, core.ErrNotFound) {
			writeDomainError(w, h.cfg.Logger, err)
			return
		}
		quota = q
	}

	now := time.Now().UTC()
	if quota == nil {
		quota = &core.TenantQuota{
			UserID:             userID,
			DailyRequestsUsed:  0,
			DailyRequestLimit:  dailyLimit,
			DailyWindowStart:   now,
			MonthlyTokensUsed:  0,
			MonthlyTokenLimit:  monthlyLimit,
			MonthlyWindowStart: now,
			UpdatedAt:          now,
		}
	} else {
		if quota.DailyRequestLimit != dailyLimit {
			quota.DailyRequestLimit = dailyLimit
		}
		if quota.MonthlyTokenLimit != monthlyLimit {
			quota.MonthlyTokenLimit = monthlyLimit
		}
	}

	dailyRemaining := quota.DailyRequestLimit - quota.DailyRequestsUsed
	if dailyRemaining < 0 {
		dailyRemaining = 0
	}
	monthlyRemaining := quota.MonthlyTokenLimit - quota.MonthlyTokensUsed
	if monthlyRemaining < 0 {
		monthlyRemaining = 0
	}

	dailyResetAt := quota.DailyWindowStart.Add(24 * time.Hour)
	monthlyResetAt := quota.MonthlyWindowStart.Add(30 * 24 * time.Hour)

	resp := QuotaResponse{
		TenantQuota:            *quota,
		DailyRequestsRemaining: dailyRemaining,
		MonthlyTokensRemaining: monthlyRemaining,
		DailyResetAt:           dailyResetAt,
		MonthlyResetAt:         monthlyResetAt,
	}

	writeJSON(w, http.StatusOK, resp)
}
