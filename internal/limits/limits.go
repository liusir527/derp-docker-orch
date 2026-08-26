// Package limits 系统资源评估与配额准入。
// 防无限量创建容器导致宿主崩溃:活跃容器数上限 + 总内存/CPU 水位。
package limits

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"derp-orch/internal/docker"
	"derp-orch/internal/store"
)

// Options 配额配置。
type Options struct {
	MaxContainers      int     // 活跃容器数上限
	MemoryWatermarkPct float64 // 宿主内存使用率水位(0-100)
	CPUWatermarkPct    float64 // 容器总 CPU% 水位
}

// Report 资源报告。
type Report struct {
	ActiveContainers int     `json:"activeContainers"`
	MaxContainers    int     `json:"maxContainers"`
	MemUsedBytes     uint64  `json:"memUsedBytes"`
	MemTotalBytes    uint64  `json:"memTotalBytes"`
	MemPct           float64 `json:"memPct"`
	CPUUsedPct       float64 `json:"cpuUsedPct"`
	CPUWatermarkPct  float64 `json:"cpuWatermarkPct"`
}

// Checker 配额检查器。缓存最近统计,避免每请求打 Docker stats(耗时)。
type Checker struct {
	opts Options
	orch docker.Orchestrator
	st   *store.Store

	mu       sync.Mutex
	cached   *Report
	cachedAt int64 // unix 秒
}

// New 创建检查器。
func New(opts Options, orch docker.Orchestrator, st *store.Store) *Checker {
	return &Checker{opts: opts, orch: orch, st: st}
}

// CanCreate 是否允许创建新容器。ok=false 时 reason 说明原因。
func (c *Checker) CanCreate(ctx context.Context) (ok bool, reason string, rep *Report, err error) {
	rep, err = c.Report(ctx)
	if err != nil {
		return false, "", nil, err
	}
	if rep.ActiveContainers >= c.opts.MaxContainers {
		return false, fmt.Sprintf("容器数量已达上限 %d,请回收过期实例后再试", c.opts.MaxContainers), rep, nil
	}
	if rep.MemPct > c.opts.MemoryWatermarkPct {
		return false, fmt.Sprintf("宿主内存使用率 %.1f%% 超过水位 %.0f%%", rep.MemPct, c.opts.MemoryWatermarkPct), rep, nil
	}
	if rep.CPUUsedPct > c.opts.CPUWatermarkPct {
		return false, fmt.Sprintf("容器总 CPU 使用率 %.1f%% 超过水位 %.0f%%", rep.CPUUsedPct, c.opts.CPUWatermarkPct), rep, nil
	}
	return true, "", rep, nil
}

// Report 计算当前资源报告。1 秒缓存。
func (c *Checker) Report(ctx context.Context) (*Report, error) {
	now := time.Now().Unix()
	c.mu.Lock()
	if c.cached != nil && now-c.cachedAt < 1 {
		rep := *c.cached
		c.mu.Unlock()
		return &rep, nil
	}
	c.mu.Unlock()

	insts, err := c.st.ListInstances(ctx)
	if err != nil {
		return nil, fmt.Errorf("查询实例: %w", err)
	}
	active := 0
	for _, i := range insts {
		if i.State == "active" {
			active++
		}
	}

	rep := &Report{
		ActiveContainers: active,
		MaxContainers:    c.opts.MaxContainers,
		MemTotalBytes:    hostMemTotal(),
		CPUWatermarkPct:  c.opts.CPUWatermarkPct,
	}

	// Docker stats 汇总内存/CPU;失败降级(仅容器数配额仍生效)。
	stats, err := c.orch.Stats(ctx)
	if err == nil {
		for _, s := range stats {
			rep.MemUsedBytes += s.MemBytes
			rep.CPUUsedPct += s.CPUPct
		}
	}
	if rep.MemTotalBytes > 0 {
		rep.MemPct = float64(rep.MemUsedBytes) / float64(rep.MemTotalBytes) * 100
	}

	c.mu.Lock()
	c.cached = rep
	c.cachedAt = now
	c.mu.Unlock()
	return rep, nil
}

// hostMemTotal 读取宿主总内存(/proc/meminfo MemTotal)。
func hostMemTotal() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				kb, _ := strconv.ParseUint(fields[1], 10, 64)
				return kb * 1024
			}
		}
	}
	return 0
}
