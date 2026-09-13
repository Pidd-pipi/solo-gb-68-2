package services

// CompleteIrrigation 写入 duration 的测试（真实 PostgreSQL，由 main_test.go 的 TestMain 提供）。
//
// 覆盖：
//   - 成功完成：duration 字段写入实际秒数，且与 end_time - start_time 一致
//   - 执行记录详情（GetIrrigationHistory）可查到该时长
//   - 记录字段与统计结果（GetWaterUsageStats）一致
//   - 失败完成：同样写入 duration，但不计入成功统计（状态逻辑不变）

import (
	"math"
	"testing"
	"time"

	"irrigation/internal/models"
	"irrigation/pkg/database"
)

// completeFlow 走真实完成流程：开始灌溉 → 将开始时间回拨 elapsed → 完成灌溉，返回记录 ID。
func completeFlow(t *testing.T, svc *IrrigationService, zoneID uint, elapsed time.Duration, success bool) uint {
	t.Helper()

	log, err := svc.StartIrrigation(nil, &zoneID, models.TriggerTypeManual)
	if err != nil {
		t.Fatalf("StartIrrigation: %v", err)
	}

	backdated := time.Now().Add(-elapsed)
	if err := database.DB.Model(&models.IrrigationLog{}).
		Where("id = ?", log.ID).
		Update("start_time", backdated).Error; err != nil {
		t.Fatalf("backdate start_time: %v", err)
	}

	var errMsg *string
	if !success {
		msg := "pump failure"
		errMsg = &msg
	}
	if err := svc.CompleteIrrigation(log.ID, success, nil, errMsg); err != nil {
		t.Fatalf("CompleteIrrigation: %v", err)
	}
	return log.ID
}

func mustLoadLog(t *testing.T, id uint) models.IrrigationLog {
	t.Helper()
	var log models.IrrigationLog
	if err := database.DB.First(&log, id).Error; err != nil {
		t.Fatalf("reload log %d: %v", id, err)
	}
	return log
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
