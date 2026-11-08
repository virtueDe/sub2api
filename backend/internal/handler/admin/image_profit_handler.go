package admin

import (
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/shopspring/decimal"
)

type ImageProfitHandler struct{ service *service.ImageProfitService }

func NewImageProfitHandler(s *service.ImageProfitService) *ImageProfitHandler {
	return &ImageProfitHandler{service: s}
}

func (h *ImageProfitHandler) ListPrices(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
	if page < 1 || size < 1 || size > 100 {
		response.BadRequest(c, "Invalid pagination")
		return
	}
	items, total, err := h.service.ListPrices(c.Request.Context(), page, size)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "total": total})
}

func (h *ImageProfitHandler) CreatePrice(c *gin.Context) {
	var req struct {
		ScopeType   string           `json:"scope_type" binding:"required"`
		ScopeID     int64            `json:"scope_id" binding:"required"`
		Model       string           `json:"model" binding:"required"`
		Resolution  string           `json:"resolution" binding:"required"`
		UnitPrice   *decimal.Decimal `json:"unit_price_usd" binding:"required"`
		EffectiveAt time.Time        `json:"effective_at"`
		Note        string           `json:"note" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid procurement price")
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "authentication required")
		return
	}
	p := service.ImageProcurementPrice{ScopeType: req.ScopeType, ScopeID: req.ScopeID, Model: req.Model, Resolution: req.Resolution, UnitPrice: *req.UnitPrice, EffectiveAt: req.EffectiveAt, Note: req.Note, CreatedBy: subject.UserID}
	if err := h.service.CreatePrice(c.Request.Context(), &p); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, p)
}

func (h *ImageProfitHandler) ListReports(c *gin.Context) {
	end := service.ImageProfitDueDate(time.Now())
	start := end.AddDate(0, 0, -29)
	items, err := h.service.ListReports(c.Request.Context(), c.DefaultQuery("start_date", start.Format("2006-01-02")), c.DefaultQuery("end_date", end.Format("2006-01-02")), c.Query("history") == "true")
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"items": items, "timezone": "Asia/Shanghai", "currency": "USD"})
}

func (h *ImageProfitHandler) GetReport(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid report ID")
		return
	}
	item, err := h.service.GetReport(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}

func (h *ImageProfitHandler) Recalculate(c *gin.Context) {
	var req struct {
		Date   string `json:"date" binding:"required"`
		Reason string `json:"reason" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Date and reason are required")
		return
	}
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Unauthorized(c, "authentication required")
		return
	}
	item, err := h.service.Recalculate(c.Request.Context(), req.Date, req.Reason, subject.UserID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, item)
}
