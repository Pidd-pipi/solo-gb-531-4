package handler

import (
	"github.com/gin-gonic/gin"
	"hazop-safeguard-coverage/backend/internal/dto"
	"hazop-safeguard-coverage/backend/internal/service"
	"hazop-safeguard-coverage/backend/internal/util"
	"net/http"
)

type RectificationItemHandler struct {
	service service.RectificationItemService
}

func NewRectificationItemHandler(value service.RectificationItemService) *RectificationItemHandler {
	return &RectificationItemHandler{service: value}
}

func (h *RectificationItemHandler) List(c *gin.Context) {
	scenarioID, ok := optionalUint(c, "scenario_id")
	if !ok {
		return
	}
	page, size := util.Pagination(c)
	result, err := h.service.List(c.Request.Context(), dto.RectificationQuery{
		ScenarioID: scenarioID, State: c.Query("state"), Page: page, PageSize: size,
	})
	respond(c, http.StatusOK, result, err)
}

func (h *RectificationItemHandler) Summary(c *gin.Context) {
	result, err := h.service.Summary(c.Request.Context())
	respond(c, http.StatusOK, result, err)
}

func (h *RectificationItemHandler) Get(c *gin.Context) {
	id, err := util.ParseUintParam(c, "id")
	if err != nil {
		util.Fail(c, err)
		return
	}
	result, err := h.service.Get(c.Request.Context(), id)
	respond(c, http.StatusOK, result, err)
}

func (h *RectificationItemHandler) Generate(c *gin.Context) {
	var request dto.GenerateRectificationRequest
	if !bindJSON(c, &request) {
		return
	}
	result, err := h.service.Generate(c.Request.Context(), request, mustActor(c))
	respond(c, http.StatusCreated, result, err)
}

func (h *RectificationItemHandler) Update(c *gin.Context) {
	id, err := util.ParseUintParam(c, "id")
	if err != nil {
		util.Fail(c, err)
		return
	}
	var request dto.UpdateRectificationRequest
	if !bindJSON(c, &request) {
		return
	}
	result, err := h.service.Update(c.Request.Context(), id, request, mustActor(c))
	respond(c, http.StatusOK, result, err)
}

func (h *RectificationItemHandler) Transition(c *gin.Context) {
	id, err := util.ParseUintParam(c, "id")
	if err != nil {
		util.Fail(c, err)
		return
	}
	var request dto.TransitionRectificationRequest
	if !bindJSON(c, &request) {
		return
	}
	result, err := h.service.Transition(c.Request.Context(), id, request, mustActor(c))
	respond(c, http.StatusOK, result, err)
}

func (h *RectificationItemHandler) Complete(c *gin.Context) {
	id, err := util.ParseUintParam(c, "id")
	if err != nil {
		util.Fail(c, err)
		return
	}
	var request dto.CompleteRectificationRequest
	if !bindJSON(c, &request) {
		return
	}
	result, err := h.service.Complete(c.Request.Context(), id, request, mustActor(c))
	respond(c, http.StatusOK, result, err)
}

func (h *RectificationItemHandler) Return(c *gin.Context) {
	id, err := util.ParseUintParam(c, "id")
	if err != nil {
		util.Fail(c, err)
		return
	}
	var request dto.ReturnRectificationRequest
	if !bindJSON(c, &request) {
		return
	}
	result, err := h.service.Return(c.Request.Context(), id, request, mustActor(c))
	respond(c, http.StatusOK, result, err)
}
