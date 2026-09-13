package controllers

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"irrigation/internal/models"
	"irrigation/internal/services"
	"irrigation/pkg/response"
)

type IrrigationController struct {
	irrigationService *services.IrrigationService
}

func NewIrrigationController() *IrrigationController {
	return &IrrigationController{
		irrigationService: services.NewIrrigationService(),
	}
}

// parseOptionalZoneID 解析可选的 zone_id 查询参数，未提供时返回 nil。
func parseOptionalZoneID(ctx *gin.Context) *uint {
	if zoneIDStr := ctx.Query("zone_id"); zoneIDStr != "" {
		id, _ := strconv.ParseUint(zoneIDStr, 10, 32)
		idUint := uint(id)
		return &idUint
	}
	return nil
}

// parseOptionalTimeRange 解析可选的 start_time/end_time 查询参数（RFC3339），
// 未提供或解析失败时对应值为零值。
func parseOptionalTimeRange(ctx *gin.Context) (startTime, endTime time.Time) {
	if startStr := ctx.Query("start_time"); startStr != "" {
		startTime, _ = time.Parse(time.RFC3339, startStr)
	}
	if endStr := ctx.Query("end_time"); endStr != "" {
		endTime, _ = time.Parse(time.RFC3339, endStr)
	}
	return startTime, endTime
}

// ManualIrrigate godoc
// @Summary 手动灌溉
// @Description 触发手动灌溉
// @Tags 灌溉执行
// @Security ApiKeyAuth
// @Accept json
// @Produce json
// @Param zone_id body int true "区域ID"
// @Success 200 {object} models.IrrigationLog
// @Router /api/irrigation/manual [post]
func (c *IrrigationController) ManualIrrigate(ctx *gin.Context) {
	var req struct {
		ZoneID uint `json:"zone_id" binding:"required"`
	}

	if err := ctx.ShouldBindJSON(&req); err != nil {
		response.BadRequest(ctx, "Invalid request body")
		return
	}

	log, err := c.irrigationService.StartIrrigation(nil, &req.ZoneID, models.TriggerTypeManual)
	if err != nil {
		response.InternalServerError(ctx, err.Error())
		return
	}

	response.Success(ctx, log)
}

// GetIrrigationHistory godoc
// @Summary 获取灌溉历史
// @Description 获取灌溉执行历史记录
// @Tags 灌溉执行
// @Security ApiKeyAuth
// @Produce json
// @Param zone_id query int false "区域ID"
// @Param start_time query string false "开始时间 (RFC3339)"
// @Param end_time query string false "结束时间 (RFC3339)"
// @Param limit query int false "返回数量限制" default(100)
// @Success 200 {array} models.IrrigationLog
// @Router /api/irrigation/history [get]
func (c *IrrigationController) GetHistory(ctx *gin.Context) {
	zoneID := parseOptionalZoneID(ctx)
	startTime, endTime := parseOptionalTimeRange(ctx)

	limit := 100
	if limitStr := ctx.Query("limit"); limitStr != "" {
		limit, _ = strconv.Atoi(limitStr)
	}

	logs, err := c.irrigationService.GetIrrigationHistory(zoneID, startTime, endTime, limit)
	if err != nil {
		response.InternalServerError(ctx, err.Error())
		return
	}

	response.Success(ctx, logs)
}

// GetWaterUsageStats godoc
// @Summary 获取用水量统计
// @Description 获取指定时间段的用水量统计
// @Tags 用水统计
// @Security ApiKeyAuth
// @Produce json
// @Param zone_id query int false "区域ID"
// @Param start_time query string false "开始时间 (RFC3339)"
// @Param end_time query string false "结束时间 (RFC3339)"
// @Success 200 {object} services.WaterUsageStats
// @Router /api/statistics/water-usage [get]
func (c *IrrigationController) GetWaterUsageStats(ctx *gin.Context) {
	zoneID := parseOptionalZoneID(ctx)
	startTime, endTime := parseOptionalTimeRange(ctx)

	stats, err := c.irrigationService.GetWaterUsageStats(zoneID, startTime, endTime)
	if err != nil {
		response.InternalServerError(ctx, err.Error())
		return
	}

	response.Success(ctx, stats)
}

// GetZoneWaterUsage godoc
// @Summary 获取各区域用水量
// @Description 获取各区域的用水量分布
// @Tags 用水统计
// @Security ApiKeyAuth
// @Produce json
// @Param start_time query string false "开始时间 (RFC3339)"
// @Param end_time query string false "结束时间 (RFC3339)"
// @Success 200 {array} services.ZoneWaterUsage
// @Router /api/statistics/zone-usage [get]
func (c *IrrigationController) GetZoneWaterUsage(ctx *gin.Context) {
	startTime, endTime := parseOptionalTimeRange(ctx)
	if ctx.Query("start_time") == "" {
		startTime = time.Now().AddDate(0, 0, -7)
	}
	if ctx.Query("end_time") == "" {
		endTime = time.Now()
	}

	usage, err := c.irrigationService.GetZoneWaterUsage(startTime, endTime)
	if err != nil {
		response.InternalServerError(ctx, err.Error())
		return
	}

	response.Success(ctx, usage)
}
