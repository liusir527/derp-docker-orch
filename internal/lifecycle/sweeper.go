// Package lifecycle 实现过期实例自动清理。
// 顺序: 删容器 → 删 DB 行。任何一步失败留待下轮重试。
package lifecycle

import (
	"context"
	"log"
	"time"

	"derp-orch/internal/docker"
	"derp-orch/internal/store"
)

// Sweeper 过期实例清理器。
type Sweeper struct {
	store    *store.Store
	orch     docker.Orchestrator
	interval time.Duration
	now      func() time.Time
	// LastSweepAt 最近一次扫描时间(含失败),供 /healthz。
	LastSweepAt time.Time
}

// New 创建 sweeper。
func New(s *store.Store, orch docker.Orchestrator, interval time.Duration) *Sweeper {
	return &Sweeper{store: s, orch: orch, interval: interval, now: time.Now}
}

// Run 阻塞运行扫描循环,直到 ctx 取消。
func (sw *Sweeper) Run(ctx context.Context) {
	ticker := time.NewTicker(sw.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sw.SweepOnce(ctx)
		}
	}
}

// SweepOnce 执行一轮清理。供测试直接调用。
func (sw *Sweeper) SweepOnce(ctx context.Context) {
	sw.LastSweepAt = sw.now()

	expired, err := sw.store.ListExpired(ctx, sw.now())
	if err != nil {
		log.Printf("[sweep] 查询过期实例失败: %v", err)
		return
	}
	if len(expired) == 0 {
		return
	}

	removed := 0
	var cleaned []string
	for _, inst := range expired {
		// 先删容器;失败则保留 DB 行,下轮重试。
		if err := sw.orch.RemoveNode(ctx, inst); err != nil {
			log.Printf("[sweep] 移除实例 %s 容器失败(下轮重试): %v", inst.ID, err)
			continue
		}
		if err := sw.store.DeleteInstance(ctx, inst.ID); err != nil {
			log.Printf("[sweep] 删除实例 %s 记录失败: %v", inst.ID, err)
			continue
		}
		removed++
		cleaned = append(cleaned, inst.ID)
	}

	if removed > 0 {
		log.Printf("[sweep] 清理 %d 个过期实例: %v", removed, cleaned)
	}
}