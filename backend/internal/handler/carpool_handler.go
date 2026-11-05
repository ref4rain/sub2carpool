package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// CarpoolHandler 拼车接口：/api/v1/carpools 面向车内成员，/api/v1/admin/carpools 面向系统管理员。
type CarpoolHandler struct {
	carpoolService *service.CarpoolService
}

func NewCarpoolHandler(carpoolService *service.CarpoolService) *CarpoolHandler {
	return &CarpoolHandler{carpoolService: carpoolService}
}

type carpoolQuotaRequest struct {
	QuotaSource    *string  `json:"quota_source" binding:"omitempty,oneof=auto"`
	WeeklyQuotaUSD *float64 `json:"weekly_quota_usd"`
	ReserveRatio   *float64 `json:"reserve_ratio"`
}

func (r *carpoolQuotaRequest) toInput() service.UpdateCarpoolQuotaInput {
	return service.UpdateCarpoolQuotaInput{
		QuotaSource:    r.QuotaSource,
		WeeklyQuotaUSD: r.WeeklyQuotaUSD,
		ReserveRatio:   r.ReserveRatio,
	}
}

type carpoolAccountActionRequest struct {
	Action      string `json:"action" binding:"required,oneof=set_schedulable clear_error clear_rate_limit refresh_usage"`
	Schedulable *bool  `json:"schedulable"`
}

type carpoolMemberRequest struct {
	UserID       int64    `json:"user_id"`
	Email        string   `json:"email"`
	Role         string   `json:"role"`
	SharePercent *float64 `json:"share_percent"`
}

func (r carpoolMemberRequest) toInput() service.CarpoolMemberInput {
	return service.CarpoolMemberInput{UserID: r.UserID, Email: r.Email, Role: r.Role, SharePercent: r.SharePercent}
}

type carpoolShareRequest struct {
	SharePercent *float64 `json:"share_percent" binding:"required"`
}

type createCarpoolRequest struct {
	Name             string                 `json:"name" binding:"required"`
	AccountID        int64                  `json:"account_id" binding:"required"`
	WeeklyQuotaUSD   *float64               `json:"weekly_quota_usd"`
	Seats            int                    `json:"seats" binding:"required"`
	ReserveRatio     float64                `json:"reserve_ratio"`
	ValidityDays     int                    `json:"validity_days"`
	Notes            string                 `json:"notes"`
	ExclusiveAccount *bool                  `json:"exclusive_account"`
	Members          []carpoolMemberRequest `json:"members"`
}

type adminUpdateCarpoolRequest struct {
	carpoolQuotaRequest
	Name         *string `json:"name"`
	Notes        *string `json:"notes"`
	Seats        *int    `json:"seats"`
	ValidityDays *int    `json:"validity_days"`
}

func carpoolIDParam(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid carpool ID")
		return 0, false
	}
	return id, true
}

func carpoolSubject(c *gin.Context) (int64, bool) {
	subject, ok := middleware2.GetAuthSubjectFromContext(c)
	if !ok {
		response.Unauthorized(c, "User not found in context")
		return 0, false
	}
	return subject.UserID, true
}

// ---------- 车内成员 ----------

// ListMine GET /api/v1/carpools
func (h *CarpoolHandler) ListMine(c *gin.Context) {
	userID, ok := carpoolSubject(c)
	if !ok {
		return
	}
	items, err := h.carpoolService.ListMine(c.Request.Context(), userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// GetDetail GET /api/v1/carpools/:id
func (h *CarpoolHandler) GetDetail(c *gin.Context) {
	userID, ok := carpoolSubject(c)
	if !ok {
		return
	}
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	detail, err := h.carpoolService.GetDetail(c.Request.Context(), id, userID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, detail)
}

// UpdateQuota PUT /api/v1/carpools/:id/quota （仅 manager）
func (h *CarpoolHandler) UpdateQuota(c *gin.Context) {
	userID, ok := carpoolSubject(c)
	if !ok {
		return
	}
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	var req carpoolQuotaRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	input := req.toInput()
	view, err := h.carpoolService.ManagerUpdateQuota(c.Request.Context(), id, userID, &input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// AccountAction POST /api/v1/carpools/:id/account/action （仅 manager）
func (h *CarpoolHandler) AccountAction(c *gin.Context) {
	userID, ok := carpoolSubject(c)
	if !ok {
		return
	}
	h.doAccountAction(c, userID)
}

func (h *CarpoolHandler) doAccountAction(c *gin.Context, userID int64) {
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	var req carpoolAccountActionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.carpoolService.AccountAction(c.Request.Context(), id, userID, req.Action, req.Schedulable); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "ok"})
}

// ---------- 系统管理员 ----------

// AdminList GET /api/v1/admin/carpools
func (h *CarpoolHandler) AdminList(c *gin.Context) {
	items, err := h.carpoolService.AdminList(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, items)
}

// AdminGetDetail GET /api/v1/admin/carpools/:id
func (h *CarpoolHandler) AdminGetDetail(c *gin.Context) {
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	detail, err := h.carpoolService.AdminGetDetail(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, detail)
}

// AdminCreate POST /api/v1/admin/carpools
func (h *CarpoolHandler) AdminCreate(c *gin.Context) {
	actorID, ok := carpoolSubject(c)
	if !ok {
		return
	}
	var req createCarpoolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	members := make([]service.CarpoolMemberInput, 0, len(req.Members))
	for _, m := range req.Members {
		members = append(members, m.toInput())
	}
	exclusive := true
	if req.ExclusiveAccount != nil {
		exclusive = *req.ExclusiveAccount
	}
	view, err := h.carpoolService.AdminCreate(c.Request.Context(), &service.CreateCarpoolInput{
		Name:             req.Name,
		AccountID:        req.AccountID,
		WeeklyQuotaUSD:   req.WeeklyQuotaUSD,
		Seats:            req.Seats,
		ReserveRatio:     req.ReserveRatio,
		ValidityDays:     req.ValidityDays,
		Notes:            req.Notes,
		ExclusiveAccount: exclusive,
		Members:          members,
		ActorID:          actorID,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Created(c, view)
}

// AdminUpdate PUT /api/v1/admin/carpools/:id
func (h *CarpoolHandler) AdminUpdate(c *gin.Context) {
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	var req adminUpdateCarpoolRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	view, err := h.carpoolService.AdminUpdate(c.Request.Context(), id, &service.AdminUpdateCarpoolInput{
		UpdateCarpoolQuotaInput: req.toInput(),
		Name:                    req.Name,
		Notes:                   req.Notes,
		Seats:                   req.Seats,
		ValidityDays:            req.ValidityDays,
	})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

// AdminDelete DELETE /api/v1/admin/carpools/:id
func (h *CarpoolHandler) AdminDelete(c *gin.Context) {
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	if err := h.carpoolService.AdminDelete(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "deleted"})
}

// AdminAddMember POST /api/v1/admin/carpools/:id/members
func (h *CarpoolHandler) AdminAddMember(c *gin.Context) {
	actorID, ok := carpoolSubject(c)
	if !ok {
		return
	}
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	var req carpoolMemberRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.carpoolService.AdminAddMember(c.Request.Context(), id, req.toInput(), actorID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "ok"})
}

func carpoolMemberUserParam(c *gin.Context) (int64, bool) {
	userID, err := strconv.ParseInt(c.Param("user_id"), 10, 64)
	if err != nil || userID <= 0 {
		response.BadRequest(c, "Invalid user ID")
		return 0, false
	}
	return userID, true
}

// AdminRemoveMember DELETE /api/v1/admin/carpools/:id/members/:user_id
func (h *CarpoolHandler) AdminRemoveMember(c *gin.Context) {
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	userID, ok := carpoolMemberUserParam(c)
	if !ok {
		return
	}
	if err := h.carpoolService.AdminRemoveMember(c.Request.Context(), id, userID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "ok"})
}

// AdminSetMemberRole PUT /api/v1/admin/carpools/:id/members/:user_id
func (h *CarpoolHandler) AdminSetMemberRole(c *gin.Context) {
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	userID, ok := carpoolMemberUserParam(c)
	if !ok {
		return
	}
	var req struct {
		Role string `json:"role" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	if err := h.carpoolService.AdminSetMemberRole(c.Request.Context(), id, userID, req.Role); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "ok"})
}

// AdminRenew POST /api/v1/admin/carpools/:id/renew
func (h *CarpoolHandler) AdminRenew(c *gin.Context) {
	actorID, ok := carpoolSubject(c)
	if !ok {
		return
	}
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	if err := h.carpoolService.AdminRenew(c.Request.Context(), id, actorID); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"message": "ok"})
}

// AdminAccountAction POST /api/v1/admin/carpools/:id/account/action
func (h *CarpoolHandler) AdminAccountAction(c *gin.Context) {
	h.doAccountAction(c, 0)
}

// ---------- 车位比例与重置卡（成员与管理员共用，actorID=0 表示系统管理员） ----------

func (h *CarpoolHandler) doSetMemberShare(c *gin.Context, actorID int64) {
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	userID, ok := carpoolMemberUserParam(c)
	if !ok {
		return
	}
	var req carpoolShareRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	view, err := h.carpoolService.SetMemberShare(c.Request.Context(), id, actorID, userID, *req.SharePercent)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, view)
}

func (h *CarpoolHandler) doGetResetCards(c *gin.Context, actorID int64) {
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	cards, err := h.carpoolService.GetResetCards(c.Request.Context(), id, actorID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cards)
}

func (h *CarpoolHandler) doUseResetCard(c *gin.Context, actorID int64) {
	id, ok := carpoolIDParam(c)
	if !ok {
		return
	}
	result, err := h.carpoolService.UseResetCard(c.Request.Context(), id, actorID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

// SetMemberShare PUT /api/v1/carpools/:id/members/:user_id/share （仅 manager）
func (h *CarpoolHandler) SetMemberShare(c *gin.Context) {
	if userID, ok := carpoolSubject(c); ok {
		h.doSetMemberShare(c, userID)
	}
}

// GetResetCards GET /api/v1/carpools/:id/reset-cards
func (h *CarpoolHandler) GetResetCards(c *gin.Context) {
	if userID, ok := carpoolSubject(c); ok {
		h.doGetResetCards(c, userID)
	}
}

// UseResetCard POST /api/v1/carpools/:id/reset-cards/use （仅 manager，整车用量 ≥ 80%）
func (h *CarpoolHandler) UseResetCard(c *gin.Context) {
	if userID, ok := carpoolSubject(c); ok {
		h.doUseResetCard(c, userID)
	}
}

// AdminSetMemberShare PUT /api/v1/admin/carpools/:id/members/:user_id/share
func (h *CarpoolHandler) AdminSetMemberShare(c *gin.Context) { h.doSetMemberShare(c, 0) }

// AdminGetResetCards GET /api/v1/admin/carpools/:id/reset-cards
func (h *CarpoolHandler) AdminGetResetCards(c *gin.Context) { h.doGetResetCards(c, 0) }

// AdminUseResetCard POST /api/v1/admin/carpools/:id/reset-cards/use
func (h *CarpoolHandler) AdminUseResetCard(c *gin.Context) { h.doUseResetCard(c, 0) }
