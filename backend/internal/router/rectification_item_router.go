package router

import (
	"hazop-safeguard-coverage/backend/internal/constants"
	"hazop-safeguard-coverage/backend/internal/handler"
	"hazop-safeguard-coverage/backend/internal/middleware"

	"github.com/gin-gonic/gin"
)

func RegisterRectificationItemRoutes(api *gin.RouterGroup, h *handler.RectificationItemHandler) {
	group := api.Group("/rectification-items")
	group.GET("", middleware.RequirePermission(constants.PermissionRead), h.List)
	group.GET("/summary", middleware.RequirePermission(constants.PermissionRead), h.Summary)
	group.GET("/:id", middleware.RequirePermission(constants.PermissionRead), h.Get)
	group.POST("/generate", middleware.RequirePermission(constants.PermissionRectification), h.Generate)
	group.PUT("/:id", middleware.RequirePermission(constants.PermissionRectification), h.Update)
	group.POST("/:id/transition", middleware.RequirePermission(constants.PermissionRectification), h.Transition)
	group.POST("/:id/complete", middleware.RequirePermission(constants.PermissionRectificationReview), h.Complete)
	group.POST("/:id/return", middleware.RequirePermission(constants.PermissionRectificationReview), h.Return)
}
