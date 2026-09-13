package services

import (
	"time"

	"gorm.io/gorm"

	"irrigation/internal/models"
	"irrigation/pkg/database"
)

type IrrigationService struct{}

func NewIrrigationService() *IrrigationService {
	return &IrrigationService{}
}

// irrigationLogFilter 封装灌溉执行记录的通用筛选条件（区域 + 时间范围），
// 供执行记录查询与用水统计复用，保证各处拼接规则一致。
type irrigationLogFilter struct {
	zoneID    *uint
	startTime time.Time
	endTime   time.Time
}

// apply 将筛选条件拼接到给定查询上，返回拼接后的查询。
func (f irrigationLogFilter) apply(query *gorm.DB) *gorm.DB {
	if f.zoneID != nil {
		query = query.Where("zone_id = ?", *f.zoneID)
	}
	if !f.startTime.IsZero() {
		query = query.Where("start_time >= ?", f.startTime)
	}
	if !f.endTime.IsZero() {
		query = query.Where("start_time <= ?", f.endTime)
	}
	return query
}

func (s *IrrigationService) StartIrrigation(scheduleID *uint, zoneID *uint, triggerType models.TriggerType) (*models.IrrigationLog, error) {
	log := &models.IrrigationLog{
		ScheduleID:  scheduleID,
		ZoneID:      zoneID,
		TriggerType: triggerType,
		StartTime:   time.Now(),
		Status:      models.ExecutionStatusInProgress,
	}

	if err := database.DB.Create(log).Error; err != nil {
		return nil, err
	}

	return log, nil
}

func (s *IrrigationService) CompleteIrrigation(logID uint, success bool, waterUsage *float64, errorMsg *string) error {
	now := time.Now()
	updates := map[string]interface{}{
		"end_time": now,
	}

	if success {
		updates["status"] = models.ExecutionStatusSuccess
	} else {
		updates["status"] = models.ExecutionStatusFailed
		if errorMsg != nil {
			updates["error_message"] = *errorMsg
		}
	}

	if waterUsage != nil {
		updates["water_usage"] = *waterUsage
	}

	return database.DB.Model(&models.IrrigationLog{}).
		Where("id = ?", logID).
		Updates(updates).Error
}

func (s *IrrigationService) GetIrrigationHistory(zoneID *uint, startTime, endTime time.Time, limit int) ([]models.IrrigationLog, error) {
	var logs []models.IrrigationLog
	query := irrigationLogFilter{zoneID: zoneID, startTime: startTime, endTime: endTime}.apply(database.DB)

	if limit > 0 {
		query = query.Limit(limit)
	}

	if err := query.Order("start_time DESC").Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

type WaterUsageStats struct {
	TotalUsage   float64 `json:"total_usage"`
	Duration     int64   `json:"duration"`
	IrrigationCount int64 `json:"irrigation_count"`
}

func (s *IrrigationService) GetWaterUsageStats(zoneID *uint, startTime, endTime time.Time) (*WaterUsageStats, error) {
	var stats WaterUsageStats
	query := irrigationLogFilter{zoneID: zoneID, startTime: startTime, endTime: endTime}.apply(
		database.DB.Model(&models.IrrigationLog{}).
			Select("COALESCE(SUM(water_usage), 0) as total_usage, COALESCE(COUNT(*), 0) as irrigation_count").
			Where("status = ?", models.ExecutionStatusSuccess),
	)

	err := query.Scan(&stats).Error
	return &stats, err
}

type ZoneWaterUsage struct {
	ZoneID     uint    `json:"zone_id"`
	ZoneName   string  `json:"zone_name"`
	WaterUsage float64 `json:"water_usage"`
	Percentage float64 `json:"percentage"`
}

// GetZoneWaterUsage 获取各区域用水量及占比：先查询各区域用水量，再统一计算占比。
func (s *IrrigationService) GetZoneWaterUsage(startTime, endTime time.Time) ([]ZoneWaterUsage, error) {
	zoneUsages, err := s.queryZoneWaterUsage(startTime, endTime)
	if err != nil {
		return nil, err
	}

	applyZoneUsagePercentages(zoneUsages)

	return zoneUsages, nil
}

// queryZoneWaterUsage 负责查询：统计时间范围内各区域的用水量（含无灌溉记录的区域）。
func (s *IrrigationService) queryZoneWaterUsage(startTime, endTime time.Time) ([]ZoneWaterUsage, error) {
	var zoneUsages []ZoneWaterUsage

	query := `
		SELECT 
			z.id as zone_id,
			z.name as zone_name,
			COALESCE(SUM(il.water_usage), 0) as water_usage
		FROM irrigation_zones z
		LEFT JOIN irrigation_logs il ON z.id = il.zone_id 
			AND il.status = 'success'
			AND il.start_time >= ? 
			AND il.start_time <= ?
		GROUP BY z.id, z.name
		ORDER BY water_usage DESC
	`

	err := database.DB.Raw(query, startTime, endTime).Scan(&zoneUsages).Error
	if err != nil {
		return nil, err
	}
	return zoneUsages, nil
}

// applyZoneUsagePercentages 负责汇总：根据各区域用水量就地计算占比（百分比），
// 占比规则集中于此，所有需要区域用水占比的入口共用。
func applyZoneUsagePercentages(zoneUsages []ZoneWaterUsage) {
	var total float64
	for _, zu := range zoneUsages {
		total += zu.WaterUsage
	}

	if total > 0 {
		for i := range zoneUsages {
			zoneUsages[i].Percentage = (zoneUsages[i].WaterUsage / total) * 100
		}
	}
}
