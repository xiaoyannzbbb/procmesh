package agent

import (
	"context"
	"log"

	"github.com/qleelulu/procmesh/internal/logmgr"
)

type diskAction int

const (
	diskActionNone diskAction = iota
	diskActionWarning
	diskActionHigh
	diskActionCritical
)

// determineDiskAction 根据磁盘使用率确定应采取的行动
func determineDiskAction(diskPercent float64) diskAction {
	switch {
	case diskPercent >= 95:
		return diskActionCritical
	case diskPercent >= 90:
		return diskActionHigh
	case diskPercent >= 85:
		return diskActionWarning
	default:
		return diskActionNone
	}
}

// checkDiskUsageAndProtect 检查磁盘使用率并触发 logmgr 保护措施
// 在 Collector 采集成功后调用
func checkDiskUsageAndProtect(ctx context.Context, logs *logmgr.Manager, diskPercent float64) {
	if logs == nil {
		return
	}

	action := determineDiskAction(diskPercent)

	switch action {
	case diskActionCritical:
		log.Printf("CRITICAL: disk usage at %.1f%%, triggering disk protection", diskPercent)
		// 调用 logmgr.Protect 触发清理和保护措施
		if _, err := logs.Protect(ctx); err != nil {
			log.Printf("disk protection failed: %v", err)
		}

	case diskActionHigh:
		log.Printf("HIGH: disk usage at %.1f%%, triggering disk protection", diskPercent)
		// 调用 logmgr.Protect 触发积极清理
		if _, err := logs.Protect(ctx); err != nil {
			log.Printf("disk protection failed: %v", err)
		}

	case diskActionWarning:
		log.Printf("WARNING: disk usage at %.1f%%", diskPercent)
		// 告警级别也调用 Protect，让 logmgr 根据其 Policy 决定是否采取行动
		if _, err := logs.Protect(ctx); err != nil {
			log.Printf("disk protection failed: %v", err)
		}

	case diskActionNone:
		// 正常，无需操作
	}
}

func historyWritesPaused(policy logmgr.Policy, diskPercent float64) bool {
	return policy.EmergencyStopWrites && diskPercent > float64(policy.EmergencyPercent)
}
