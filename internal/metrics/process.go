package metrics

import (
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v3/process"
)

// collectProcess 采集单个进程的资源指标
// 此函数轻量且不阻塞，返回瞬时值
func collectProcess(pid int) (*ProcessMetrics, error) {
	if pid <= 0 {
		return nil, fmt.Errorf("invalid pid: %d", pid)
	}

	p, err := process.NewProcess(int32(pid))
	if err != nil {
		return nil, fmt.Errorf("process not found: %w", err)
	}

	// CPU（瞬时值，不阻塞）
	cpuPct, err := p.CPUPercent()
	if err != nil {
		return nil, fmt.Errorf("cpu percent: %w", err)
	}

	// Memory（RSS）
	memInfo, err := p.MemoryInfo()
	if err != nil {
		return nil, fmt.Errorf("memory info: %w", err)
	}

	return &ProcessMetrics{
		PID:         pid,
		CPUPercent:  cpuPct,
		MemoryBytes: memInfo.RSS,
		Timestamp:   time.Now(),
	}, nil
}
