package handler

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *ModelPlazaHandler) GetDisplayGroupModels(c *gin.Context) {
	groupID, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || groupID <= 0 {
		response.BadRequest(c, "Invalid group ID")
		return
	}
	models, err := h.plazaService.ListDisplayGroupModels(c.Request.Context(), groupID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, models)
}

// These endpoints are mounted exclusively under the administrator middleware.
func (h *ModelPlazaHandler) GetDisplayConfig(c *gin.Context) {
	cfg, err := h.settingService.GetModelPlazaDisplay(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}

func (h *ModelPlazaHandler) UpdateDisplayConfig(c *gin.Context) {
	var input service.ModelPlazaDisplayConfig
	if err := c.ShouldBindJSON(&input); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	cfg, err := h.plazaService.ValidateDisplayConfig(c.Request.Context(), input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err := h.settingService.SetModelPlazaDisplay(c.Request.Context(), cfg); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, cfg)
}
