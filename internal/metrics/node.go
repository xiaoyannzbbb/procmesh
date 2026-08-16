package metrics

import (
	"fmt"
	"time"

	"github.com/shirou/gopsutil/v3/cpu"
	"github.com/shirou/gopsutil/v3/disk"
	"github.com/shirou/gopsutil/v3/mem"
)

// collectNode 采集节点级资源指标
// dataDir: 数据目录路径，用于确定监控哪个磁盘分区
// 注意：此函数会阻塞约 1 秒（CPU 采样需要）
func collectNode(dataDir string) (*NodeMetrics, error) {
	// CPU：采样 1 秒，获取总体使用率
	cpuPct, err := cpu.Percent(time.Second, false) // false = 总体，不按核
	if err != nil {
		return nil, fmt.Errorf("cpu percent: %w", err)
	}
	if len(cpuPct) == 0 {
		return nil, fmt.Errorf("cpu percent returned empty array")
	}

	// Memory
	vmem, err := mem.VirtualMemory()
	if err != nil {
		return nil, fmt.Errorf("memory: %w", err)
	}

	// Disk：数据目录所在分区
	diskStat, err := disk.Usage(dataDir)
	if err != nil {
		return nil, fmt.Errorf("disk usage for %s: %w", dataDir, err)
	}

	return &NodeMetrics{
		CPUPercent:    cpuPct[0],
		MemoryPercent: vmem.UsedPercent,
		MemoryUsed:    vmem.Used,
		MemoryTotal:   vmem.Total,
		DiskPercent:   diskStat.UsedPercent,
		DiskUsed:      diskStat.Used,
		DiskTotal:     diskStat.Total,
		Timestamp:     time.Now(),
	}, nil
}
