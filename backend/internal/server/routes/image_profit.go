package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

func registerImageProfitRoutes(admin *gin.RouterGroup, h *handler.Handlers) {
	if h.Admin.ImageProfit == nil {
		return
	}
	g := admin.Group("/profit-accounting")
	g.GET("/prices", h.Admin.ImageProfit.ListPrices)
	g.POST("/prices", h.Admin.ImageProfit.CreatePrice)
	g.GET("/reports", h.Admin.ImageProfit.ListReports)
	g.GET("/reports/:id", h.Admin.ImageProfit.GetReport)
	g.POST("/recalculate", h.Admin.ImageProfit.Recalculate)
}
