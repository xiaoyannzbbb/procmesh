package metrics

import (
	"context"
	"sync"
	"time"
)

// NodeMetrics 节点级资源指标
type NodeMetrics struct {
	CPUPercent    float64   // 整机 CPU 使用率 0-100
	MemoryPercent float64   // 内存使用率 0-100
	MemoryUsed    uint64    // 已用内存（字节）
	MemoryTotal   uint64    // 总内存（字节）
	DiskPercent   float64   // 磁盘使用率 0-100（数据目录所在分区）
	DiskUsed      uint64    // 已用磁盘（字节）
	DiskTotal     uint64    // 总磁盘（字节）
	Timestamp     time.Time // 采集时间
}

// ProcessMetrics 进程级资源指标
type ProcessMetrics struct {
	PID         int       // 进程 ID
	CPUPercent  float64   // 进程 CPU 使用率 0-100（单核）
	MemoryBytes uint64    // 进程内存（RSS）
	Timestamp   time.Time // 采集时间
}

// Collector 资源采集器
type Collector struct {
	interval   time.Duration      // 采集间隔
	dataDir    string             // 数据目录路径（用于磁盘监控）
	cancel     context.CancelFunc // 停止信号
	mu         sync.RWMutex       // 保护缓存
	node       *NodeMetrics       // 缓存的节点指标
	nodeErr    error              // 最近一次采集错误
	lastUpdate time.Time          // 最后更新时间
}
