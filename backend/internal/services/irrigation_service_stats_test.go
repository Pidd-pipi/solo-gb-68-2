package services

// GetWaterUsageStats 的时长统计测试（真实 PostgreSQL，由 main_test.go 的 TestMain 提供）。
//
// 覆盖：
//   - 成功记录的时长累计（含跨区域合计、亚秒取整）
//   - 零时长记录（计入次数、时长为 0）
//   - 失败/进行中记录不计入
//   - 区域筛选、时间筛选及其组合
//   - 执行结束后时长可被查询（走 StartIrrigation/CompleteIrrigation 真实写入路径）
//   - 空结果集各统计值为 0

import (
	"math"
	"testing"
	"time"

	"irrigation/internal/models"
	"irrigation/pkg/database"
)

// resetIrrigationTables 清空灌溉相关表并重建两个区域，保证用例间互不影响、可重复运行。
func resetIrrigationTables(t *testing.T) {
	t.Helper()
	if err := database.DB.Exec(`TRUNCATE irrigation_logs, irrigation_zones RESTART IDENTITY CASCADE`).Error; err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := database.DB.Exec(`INSERT INTO irrigation_zones (id, name) VALUES (1, '玫瑰园'), (2, '草坪区')`).Error; err != nil {
		t.Fatalf("insert zones: %v", err)
	}
}

// insertLog 直接写入一条灌溉记录；end 为零值时 end_time 为 NULL（模拟进行中）。
func insertLog(t *testing.T, zoneID uint, status models.ExecutionStatus, start, end time.Time, waterUsage *float64) {
	t.Helper()
	log := models.IrrigationLog{
		ZoneID:      &zoneID,
		TriggerType: models.TriggerTypeTimed,
		StartTime:   start,
		Status:      status,
		WaterUsage:  waterUsage,
	}
	if !end.IsZero() {
		log.EndTime = &end
	}
	if err := database.DB.Create(&log).Error; err != nil {
		t.Fatalf("insert log: %v", err)
	}
}

func f64(v float64) *float64 { return &v }

func tm(s string) time.Time {
	r, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.UTC)
	if err != nil {
		panic(err)
	}
	return r
}

func almostEq(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestGetWaterUsageStats_Duration(t *testing.T) {
	svc := NewIrrigationService()
	zone1, zone2 := uint(1), uint(2)

	cases := []struct {
		name         string
		seed         func(t *testing.T)
		zoneID       *uint
		start, end   time.Time
		wantCount    int64
		wantDuration int64
		wantUsage    float64
	}{
		{
			name: "成功记录时长累计_多区域多记录",
			seed: func(t *testing.T) {
				insertLog(t, 1, models.ExecutionStatusSuccess, tm("2026-09-10 10:00:00"), tm("2026-09-10 10:10:00"), f64(60.50)) // 600s
				insertLog(t, 1, models.ExecutionStatusSuccess, tm("2026-09-11 08:00:00"), tm("2026-09-11 08:05:30"), f64(33.25)) // 330s
				insertLog(t, 2, models.ExecutionStatusSuccess, tm("2026-09-12 12:00:00"), tm("2026-09-12 12:02:00"), f64(12.00)) // 120s
			},
			zoneID:       nil,
			start:        tm("2026-09-01 00:00:00"),
			end:          tm("2026-09-30 23:59:59"),
			wantCount:    3,
			wantDuration: 1050,
			wantUsage:    105.75,
		},
		{
			name: "零时长记录_计入次数_时长为零",
			seed: func(t *testing.T) {
				insertLog(t, 1, models.ExecutionStatusSuccess, tm("2026-09-12 06:00:00"), tm("2026-09-12 06:00:00"), f64(0)) // 0s
			},
			zoneID:       nil,
			start:        tm("2026-09-01 00:00:00"),
			end:          tm("2026-09-30 23:59:59"),
			wantCount:    1,
			wantDuration: 0,
			wantUsage:    0,
		},
		{
			name: "失败与进行中记录不计入",
			seed: func(t *testing.T) {
				insertLog(t, 1, models.ExecutionStatusSuccess, tm("2026-09-10 10:00:00"), tm("2026-09-10 10:01:00"), f64(6.00)) // 60s
				insertLog(t, 1, models.ExecutionStatusFailed, tm("2026-09-11 09:00:00"), tm("2026-09-11 09:07:00"), nil)        // 失败 420s
				insertLog(t, 1, models.ExecutionStatusInProgress, tm("2026-09-12 07:00:00"), time.Time{}, nil)                  // 进行中
			},
			zoneID:       nil,
			start:        tm("2026-09-01 00:00:00"),
			end:          tm("2026-09-30 23:59:59"),
			wantCount:    1,
			wantDuration: 60,
			wantUsage:    6.00,
		},
		{
			name: "区域筛选_仅统计指定区域",
			seed: func(t *testing.T) {
				insertLog(t, 1, models.ExecutionStatusSuccess, tm("2026-09-10 10:00:00"), tm("2026-09-10 10:10:00"), f64(60.00)) // 600s
				insertLog(t, 2, models.ExecutionStatusSuccess, tm("2026-09-10 11:00:00"), tm("2026-09-10 11:05:00"), f64(30.00)) // 300s
			},
			zoneID:       &zone1,
			start:        tm("2026-09-01 00:00:00"),
			end:          tm("2026-09-30 23:59:59"),
			wantCount:    1,
			wantDuration: 600,
			wantUsage:    60.00,
		},
		{
			name: "区域筛选_另一区域互不影响",
			seed: func(t *testing.T) {
				insertLog(t, 1, models.ExecutionStatusSuccess, tm("2026-09-10 10:00:00"), tm("2026-09-10 10:10:00"), f64(60.00)) // 600s
				insertLog(t, 2, models.ExecutionStatusSuccess, tm("2026-09-10 11:00:00"), tm("2026-09-10 11:05:00"), f64(30.00)) // 300s
			},
			zoneID:       &zone2,
			start:        tm("2026-09-01 00:00:00"),
			end:          tm("2026-09-30 23:59:59"),
			wantCount:    1,
			wantDuration: 300,
			wantUsage:    30.00,
		},
		{
			name: "时间筛选_仅统计范围内记录",
			seed: func(t *testing.T) {
				insertLog(t, 1, models.ExecutionStatusSuccess, tm("2026-09-10 10:00:00"), tm("2026-09-10 10:10:00"), f64(60.00)) // 600s 范围内
				insertLog(t, 1, models.ExecutionStatusSuccess, tm("2026-09-20 10:00:00"), tm("2026-09-20 10:05:00"), f64(30.00)) // 300s 范围外
				insertLog(t, 1, models.ExecutionStatusSuccess, tm("2026-08-01 10:00:00"), tm("2026-08-01 10:03:00"), f64(18.00)) // 180s 范围外
			},
			zoneID:       nil,
			start:        tm("2026-09-01 00:00:00"),
			end:          tm("2026-09-15 23:59:59"),
			wantCount:    1,
			wantDuration: 600,
			wantUsage:    60.00,
		},
		{
			name: "区域与时间筛选组合",
			seed: func(t *testing.T) {
				insertLog(t, 1, models.ExecutionStatusSuccess, tm("2026-09-10 10:00:00"), tm("2026-09-10 10:10:00"), f64(60.00)) // 命中 600s
				insertLog(t, 1, models.ExecutionStatusSuccess, tm("2026-08-01 10:00:00"), tm("2026-08-01 10:03:00"), f64(18.00)) // 同区域但范围外
				insertLog(t, 2, models.ExecutionStatusSuccess, tm("2026-09-10 11:00:00"), tm("2026-09-10 11:05:00"), f64(30.00)) // 范围内但其他区域
			},
			zoneID:       &zone1,
			start:        tm("2026-09-01 00:00:00"),
			end:          tm("2026-09-30 23:59:59"),
			wantCount:    1,
			wantDuration: 600,
			wantUsage:    60.00,
		},
		{
			name:         "空结果集_各统计值为零",
			seed:         func(t *testing.T) {},
			zoneID:       nil,
			start:        tm("2026-01-01 00:00:00"),
			end:          tm("2026-01-02 00:00:00"),
			wantCount:    0,
			wantDuration: 0,
			wantUsage:    0,
		},
		{
			name: "亚秒时长_合计后取整到整秒",
			seed: func(t *testing.T) {
				// 两条各 1.4s，合计 2.8s，::bigint 四舍五入为 3s
				insertLog(t, 1, models.ExecutionStatusSuccess,
					time.Date(2026, 9, 10, 10, 0, 0, 0, time.UTC), time.Date(2026, 9, 10, 10, 0, 1, 400_000_000, time.UTC), f64(1.00))
				insertLog(t, 1, models.ExecutionStatusSuccess,
					time.Date(2026, 9, 11, 10, 0, 0, 0, time.UTC), time.Date(2026, 9, 11, 10, 0, 1, 400_000_000, time.UTC), f64(1.00))
			},
			zoneID:       nil,
			start:        tm("2026-09-01 00:00:00"),
			end:          tm("2026-09-30 23:59:59"),
			wantCount:    2,
			wantDuration: 3,
			wantUsage:    2.00,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetIrrigationTables(t)
			tc.seed(t)

			stats, err := svc.GetWaterUsageStats(tc.zoneID, tc.start, tc.end)
			if err != nil {
				t.Fatalf("GetWaterUsageStats: %v", err)
			}
			if stats.IrrigationCount != tc.wantCount {
				t.Errorf("irrigation_count = %d, want %d", stats.IrrigationCount, tc.wantCount)
			}
			if stats.Duration != tc.wantDuration {
				t.Errorf("duration = %d, want %d", stats.Duration, tc.wantDuration)
			}
			if !almostEq(stats.TotalUsage, tc.wantUsage) {
				t.Errorf("total_usage = %v, want %v", stats.TotalUsage, tc.wantUsage)
			}
		})
	}
}

// TestGetWaterUsageStats_DurationAfterCompletion 走真实写入路径：
// StartIrrigation 开始 → CompleteIrrigation 结束后，统计中应能查到该次灌溉的时长。
func TestGetWaterUsageStats_DurationAfterCompletion(t *testing.T) {
	resetIrrigationTables(t)
	svc := NewIrrigationService()
	zoneID := uint(1)

	log, err := svc.StartIrrigation(nil, &zoneID, models.TriggerTypeManual)
	if err != nil {
		t.Fatalf("StartIrrigation: %v", err)
	}

	// 将开始时间回拨 5 秒，使结束后的时长可预期（约 5 秒）
	backdated := time.Now().Add(-5 * time.Second)
	if err := database.DB.Model(&models.IrrigationLog{}).
		Where("id = ?", log.ID).
		Update("start_time", backdated).Error; err != nil {
		t.Fatalf("backdate start_time: %v", err)
	}

	water := 2.50
	if err := svc.CompleteIrrigation(log.ID, true, &water, nil); err != nil {
		t.Fatalf("CompleteIrrigation: %v", err)
	}

	// 结束后记录应已写入 end_time
	var finished models.IrrigationLog
	if err := database.DB.First(&finished, log.ID).Error; err != nil {
		t.Fatalf("reload log: %v", err)
	}
	if finished.EndTime == nil {
		t.Fatal("end_time is nil after CompleteIrrigation")
	}

	stats, err := svc.GetWaterUsageStats(&zoneID, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetWaterUsageStats: %v", err)
	}
	if stats.IrrigationCount != 1 {
		t.Errorf("irrigation_count = %d, want 1", stats.IrrigationCount)
	}
	// 回拨 5 秒 + 执行耗时，允许少量时间偏差
	if stats.Duration < 4 || stats.Duration > 10 {
		t.Errorf("duration = %d, want within [4, 10] seconds", stats.Duration)
	}
	if !almostEq(stats.TotalUsage, 2.50) {
		t.Errorf("total_usage = %v, want 2.5", stats.TotalUsage)
	}
}
