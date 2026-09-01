package metrics

import (
	"context"
	"fmt"
	"log"
	"time"
)

type nodeSampler func(string) (*NodeMetrics, error)

type collectorTicker interface {
	Chan() <-chan time.Time
	Stop()
}

type tickerFactory func(time.Duration) collectorTicker

type realTicker struct {
	*time.Ticker
}

func (t realTicker) Chan() <-chan time.Time { return t.C }

// New 创建新的 Collector 实例（未启动）
func New(dataDir string, interval time.Duration) *Collector {
	return &Collector{
		interval:   interval,
		dataDir:    dataDir,
		sampleNode: collectNode,
		newTicker: func(interval time.Duration) collectorTicker {
			return realTicker{Ticker: time.NewTicker(interval)}
		},
	}
}

// Start 启动后台采集协程
func (c *Collector) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	c.lifecycleMu.Lock()
	c.cancel = cancel
	c.lifecycleMu.Unlock()
	ticker := c.newTicker(c.interval)

	go func() {
		defer ticker.Stop()

		c.collect(ctx)
		for {
			select {
			case <-ticker.Chan():
				c.collect(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

// Stop 停止后台采集
func (c *Collector) Stop() {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
}

// collect 执行一次采集（内部方法）
func (c *Collector) collect(ctx context.Context) {
	node, err := c.sampleNode(c.dataDir)
	if ctx.Err() != nil {
		return
	}

	c.mu.Lock()
	c.node = node
	c.nodeErr = err
	c.lastUpdate = time.Now()
	c.mu.Unlock()

	if err != nil {
		log.Printf("metrics collection failed: %v", err)
	}
}

// NodeMetrics 获取缓存的节点指标（非阻塞）
func (c *Collector) NodeMetrics() (*NodeMetrics, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.nodeErr != nil {
		return nil, c.nodeErr
	}
	if c.node == nil {
		return nil, fmt.Errorf("no metrics available")
	}

	// 返回副本，避免外部修改
	nm := *c.node
	return &nm, nil
}

// ProcessMetrics 获取进程指标（实时采集，轻量）
func (c *Collector) ProcessMetrics(pid int) (*ProcessMetrics, error) {
	return collectProcess(pid)
}
