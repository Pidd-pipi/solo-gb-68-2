package services

// CompleteIrrigation 写入 duration 的测试（真实 PostgreSQL，由 main_test.go 的 TestMain 提供）。
//
// 覆盖：
//   - 成功完成：duration 字段写入实际秒数，且与 end_time - start_time 一致
//   - 执行记录详情（GetIrrigationHistory）可查到该时长
//   - 记录字段与统计结果（GetWaterUsageStats）一致
//   - 失败完成：同样写入 duration，但不计入成功统计（状态逻辑不变）
//   - 同一记录再次完成：以最后一次完成结果为准
//   - 成功完成后再次以失败结束：状态翻转，不再计入统计
//   - 零秒完成：duration 写入 0 而非 NULL

import (
	"math"
	"testing"
	"time"

	"irrigation/internal/models"
	"irrigation/pkg/database"
)

// startLog 通过真实服务开始一次灌溉，返回记录 ID。
func startLog(t *testing.T, svc *IrrigationService, zoneID uint) uint {
	t.Helper()
	log, err := svc.StartIrrigation(nil, &zoneID, models.TriggerTypeManual)
	if err != nil {
		t.Fatalf("StartIrrigation: %v", err)
	}
	return log.ID
}

// backdateStartTime 将记录的开始时间回拨 elapsed，使完成后的时长可预期。
func backdateStartTime(t *testing.T, logID uint, elapsed time.Duration) {
	t.Helper()
	backdated := time.Now().Add(-elapsed)
	if err := database.DB.Model(&models.IrrigationLog{}).
		Where("id = ?", logID).
		Update("start_time", backdated).Error; err != nil {
		t.Fatalf("backdate start_time: %v", err)
	}
}

// completeFlow 走真实完成流程：开始灌溉 → 回拨开始时间 → 完成灌溉，返回记录 ID。
func completeFlow(t *testing.T, svc *IrrigationService, zoneID uint, elapsed time.Duration, success bool) uint {
	t.Helper()
	logID := startLog(t, svc, zoneID)
	backdateStartTime(t, logID, elapsed)

	var errMsg *string
	if !success {
		msg := "pump failure"
		errMsg = &msg
	}
	if err := svc.CompleteIrrigation(logID, success, nil, errMsg); err != nil {
		t.Fatalf("CompleteIrrigation: %v", err)
	}
	return logID
}

func mustLoadLog(t *testing.T, id uint) models.IrrigationLog {
	t.Helper()
	var log models.IrrigationLog
	if err := database.DB.First(&log, id).Error; err != nil {
		t.Fatalf("reload log %d: %v", id, err)
	}
	return log
}

// assertZoneStatsConsistent 断言记录与统计始终一致：
//  1. 每条已完成记录的 duration 字段等于其 end_time - start_time（四舍五入到整秒）；
//  2. 统计结果等于从记录表重算的值（仅成功记录计入次数、时长、用水量）。
func assertZoneStatsConsistent(t *testing.T, svc *IrrigationService, zoneID uint) {
	t.Helper()

	var logs []models.IrrigationLog
	if err := database.DB.Where("zone_id = ?", zoneID).Find(&logs).Error; err != nil {
		t.Fatalf("load logs: %v", err)
	}

	var wantCount, wantDuration int64
	var wantUsage float64
	for _, l := range logs {
		if l.EndTime == nil || l.Duration == nil {
			continue // 进行中的记录不参与
		}
		if want := int(math.Round(l.EndTime.Sub(l.StartTime).Seconds())); *l.Duration != want {
			t.Errorf("log %d: duration = %d, want %d (from end_time - start_time)", l.ID, *l.Duration, want)
		}
		if l.Status == models.ExecutionStatusSuccess {
			wantCount++
			wantDuration += int64(*l.Duration)
			if l.WaterUsage != nil {
				wantUsage += *l.WaterUsage
			}
		}
	}

	stats, err := svc.GetWaterUsageStats(&zoneID, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetWaterUsageStats: %v", err)
	}
	if stats.IrrigationCount != wantCount {
		t.Errorf("stats irrigation_count = %d, want %d (recomputed from records)", stats.IrrigationCount, wantCount)
	}
	if stats.Duration != wantDuration {
		t.Errorf("stats duration = %d, want %d (recomputed from records)", stats.Duration, wantDuration)
	}
	if !almostEq(stats.TotalUsage, wantUsage) {
		t.Errorf("stats total_usage = %v, want %v (recomputed from records)", stats.TotalUsage, wantUsage)
	}
}

func TestCompleteIrrigation_WritesDuration(t *testing.T) {
	resetIrrigationTables(t)
	svc := NewIrrigationService()
	zoneID := uint(1)

	// 一条成功完成（约 90 秒），一条失败完成（约 30 秒）
	successID := completeFlow(t, svc, zoneID, 90*time.Second, true)
	failedID := completeFlow(t, svc, zoneID, 30*time.Second, false)

	t.Run("成功记录_duration字段与起止时间一致", func(t *testing.T) {
		log := mustLoadLog(t, successID)
		if log.Duration == nil {
			t.Fatal("duration is nil after successful CompleteIrrigation")
		}
		if log.EndTime == nil {
			t.Fatal("end_time is nil after CompleteIrrigation")
		}
		// 用读回的起止时间复算，口径：四舍五入到整秒（与数据库 ::integer 一致）
		want := int(math.Round(log.EndTime.Sub(log.StartTime).Seconds()))
		if *log.Duration != want {
			t.Errorf("duration = %d, want %d (from end_time - start_time)", *log.Duration, want)
		}
		// 回拨 90 秒 + 执行耗时，允许少量偏差
		if *log.Duration < 89 || *log.Duration > 95 {
			t.Errorf("duration = %d, want within [89, 95] seconds", *log.Duration)
		}
	})

	t.Run("失败记录_同样写入duration", func(t *testing.T) {
		log := mustLoadLog(t, failedID)
		if log.Duration == nil {
			t.Fatal("duration is nil after failed CompleteIrrigation")
		}
		if log.Status != models.ExecutionStatusFailed {
			t.Errorf("status = %q, want %q", log.Status, models.ExecutionStatusFailed)
		}
		want := int(math.Round(log.EndTime.Sub(log.StartTime).Seconds()))
		if *log.Duration != want {
			t.Errorf("duration = %d, want %d (from end_time - start_time)", *log.Duration, want)
		}
	})

	t.Run("执行记录详情_可查到时长", func(t *testing.T) {
		logs, err := svc.GetIrrigationHistory(&zoneID, time.Time{}, time.Time{}, 0)
		if err != nil {
			t.Fatalf("GetIrrigationHistory: %v", err)
		}
		if len(logs) != 2 {
			t.Fatalf("history returned %d logs, want 2", len(logs))
		}
		for _, l := range logs {
			if l.Duration == nil {
				t.Errorf("history log %d: duration is nil", l.ID)
			}
		}
	})

	t.Run("记录字段与统计结果一致", func(t *testing.T) {
		successLog := mustLoadLog(t, successID)

		stats, err := svc.GetWaterUsageStats(&zoneID, time.Time{}, time.Time{})
		if err != nil {
			t.Fatalf("GetWaterUsageStats: %v", err)
		}
		// 仅成功记录计入统计：次数为 1，统计时长等于该记录的 duration 字段
		if stats.IrrigationCount != 1 {
			t.Errorf("irrigation_count = %d, want 1 (failed record must not be counted)", stats.IrrigationCount)
		}
		if stats.Duration != int64(*successLog.Duration) {
			t.Errorf("stats duration = %d, want %d (record duration field)", stats.Duration, *successLog.Duration)
		}
	})
}

// TestCompleteIrrigation_RecompleteLastWriteWins 同一记录再次完成：记录以最后一次完成为准，
// 且是更新而非新增（统计次数始终为 1）。
func TestCompleteIrrigation_RecompleteLastWriteWins(t *testing.T) {
	resetIrrigationTables(t)
	svc := NewIrrigationService()
	zoneID := uint(1)

	logID := startLog(t, svc, zoneID)

	// 第一次完成：约 60 秒，用水 5.0
	backdateStartTime(t, logID, 60*time.Second)
	water1 := 5.0
	if err := svc.CompleteIrrigation(logID, true, &water1, nil); err != nil {
		t.Fatalf("first CompleteIrrigation: %v", err)
	}

	// 再次完成同一记录：约 120 秒，用水 8.0
	backdateStartTime(t, logID, 120*time.Second)
	water2 := 8.0
	if err := svc.CompleteIrrigation(logID, true, &water2, nil); err != nil {
		t.Fatalf("second CompleteIrrigation: %v", err)
	}

	log := mustLoadLog(t, logID)
	if log.Duration == nil {
		t.Fatal("duration is nil after re-completion")
	}
	// 最后一次结果为准：时长约 120 秒（不是第一次的 60，也不是累加的 180）
	if *log.Duration < 119 || *log.Duration > 125 {
		t.Errorf("duration = %d, want within [119, 125] (last completion wins)", *log.Duration)
	}
	if log.WaterUsage == nil {
		t.Fatal("water_usage is nil after re-completion")
	}
	if !almostEq(*log.WaterUsage, 8.0) {
		t.Errorf("water_usage = %v, want 8.0 (last completion wins)", *log.WaterUsage)
	}

	// 同一记录被更新而非新增：统计次数为 1，时长等于最后一次的记录值
	stats, err := svc.GetWaterUsageStats(&zoneID, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetWaterUsageStats: %v", err)
	}
	if stats.IrrigationCount != 1 {
		t.Errorf("irrigation_count = %d, want 1 (re-completion must update, not duplicate)", stats.IrrigationCount)
	}
	if stats.Duration != int64(*log.Duration) {
		t.Errorf("stats duration = %d, want %d (record duration field)", stats.Duration, *log.Duration)
	}

	assertZoneStatsConsistent(t, svc, zoneID)
}

// TestCompleteIrrigation_SuccessThenFailure 成功完成后再次以失败结束：
// 状态以最后一次为准翻转为失败，记录不再计入成功统计。
func TestCompleteIrrigation_SuccessThenFailure(t *testing.T) {
	resetIrrigationTables(t)
	svc := NewIrrigationService()
	zoneID := uint(1)

	logID := startLog(t, svc, zoneID)
	backdateStartTime(t, logID, 60*time.Second)
	water := 5.0
	if err := svc.CompleteIrrigation(logID, true, &water, nil); err != nil {
		t.Fatalf("CompleteIrrigation(success): %v", err)
	}

	// 成功时：统计包含该记录
	stats, err := svc.GetWaterUsageStats(&zoneID, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetWaterUsageStats: %v", err)
	}
	if stats.IrrigationCount != 1 || stats.Duration < 59 || stats.Duration > 65 || !almostEq(stats.TotalUsage, 5.0) {
		t.Errorf("after success: stats = %+v, want count=1, duration within [59, 65], usage=5.0", stats)
	}

	// 再次以失败结束同一记录
	errMsg := "pump failure"
	if err := svc.CompleteIrrigation(logID, false, nil, &errMsg); err != nil {
		t.Fatalf("CompleteIrrigation(failure): %v", err)
	}

	log := mustLoadLog(t, logID)
	if log.Status != models.ExecutionStatusFailed {
		t.Errorf("status = %q, want %q (last completion wins)", log.Status, models.ExecutionStatusFailed)
	}
	if log.ErrorMessage == nil || *log.ErrorMessage != errMsg {
		t.Errorf("error_message = %v, want %q", log.ErrorMessage, errMsg)
	}
	// duration 字段仍保留实际秒数（约 60）
	if log.Duration == nil {
		t.Fatal("duration is nil after failed re-completion")
	}
	if *log.Duration < 59 || *log.Duration > 65 {
		t.Errorf("duration = %d, want within [59, 65]", *log.Duration)
	}

	// 状态逻辑不变：仅统计 success，该记录不再计入
	statsAfter, err := svc.GetWaterUsageStats(&zoneID, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetWaterUsageStats: %v", err)
	}
	if statsAfter.IrrigationCount != 0 || statsAfter.Duration != 0 || !almostEq(statsAfter.TotalUsage, 0) {
		t.Errorf("after failure: stats = %+v, want count=0, duration=0, usage=0", statsAfter)
	}

	assertZoneStatsConsistent(t, svc, zoneID)
}

// TestCompleteIrrigation_ZeroDuration 零秒完成：duration 写入 0 而非 NULL，
// 记录仍计入次数，时长贡献为 0。
func TestCompleteIrrigation_ZeroDuration(t *testing.T) {
	resetIrrigationTables(t)
	svc := NewIrrigationService()
	zoneID := uint(1)

	// 开始后立即完成：起止时间几乎相同（远小于 0.5 秒，取整后为 0）
	logID := startLog(t, svc, zoneID)
	water := 1.5
	if err := svc.CompleteIrrigation(logID, true, &water, nil); err != nil {
		t.Fatalf("CompleteIrrigation: %v", err)
	}

	log := mustLoadLog(t, logID)
	if log.Duration == nil {
		t.Fatal("duration is nil after zero-second completion, want 0")
	}
	if *log.Duration != 0 {
		t.Errorf("duration = %d, want 0", *log.Duration)
	}

	stats, err := svc.GetWaterUsageStats(&zoneID, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("GetWaterUsageStats: %v", err)
	}
	if stats.IrrigationCount != 1 {
		t.Errorf("irrigation_count = %d, want 1", stats.IrrigationCount)
	}
	if stats.Duration != 0 {
		t.Errorf("stats duration = %d, want 0", stats.Duration)
	}
	if !almostEq(stats.TotalUsage, 1.5) {
		t.Errorf("total_usage = %v, want 1.5", stats.TotalUsage)
	}

	assertZoneStatsConsistent(t, svc, zoneID)
}
