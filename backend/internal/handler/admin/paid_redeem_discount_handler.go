package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) SetPaidRedeemDiscountService(s *service.PaidRedeemDiscountService) {
	h.paidDiscountService = s
}

func (h *SettingHandler) GetPaidRedeemDiscount(c *gin.Context) {
	result, err := h.paidDiscountService.GetConfig(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *SettingHandler) UpdatePaidRedeemDiscount(c *gin.Context) {
	var cfg service.PaidRedeemDiscountConfig
	if err := c.ShouldBindJSON(&cfg); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.paidDiscountService.UpdateConfig(c.Request.Context(), cfg)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *SettingHandler) PreviewPaidRedeemDiscount(c *gin.Context) {
	result, err := h.paidDiscountService.Preview(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}

func (h *SettingHandler) BackfillPaidRedeemDiscount(c *gin.Context) {
	var req struct {
		PreviewToken string `json:"preview_token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	result, err := h.paidDiscountService.Backfill(c.Request.Context(), req.PreviewToken)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
