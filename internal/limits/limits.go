// Package limits 系统资源评估与配额准入。
// 防无限量创建容器导致宿主崩溃:活跃容器数上限 + 总内存/CPU 水位。
// 多地区下按地区分别统计:本地读 /proc/meminfo,远程由 regiond 上报。
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
	MaxContainers      int     // 活跃容器数上限(地区未单独配置时使用)
	MemoryWatermarkPct float64 // 宿主内存使用率水位(0-100)
	CPUWatermarkPct    float64 // 容器总 CPU% 水位
}

// Report 资源报告。
type Report struct {
	Region           string  `json:"region,omitempty"`
	RegionName       string  `json:"regionName,omitempty"`
	ActiveContainers int     `json:"activeContainers"`
	MaxContainers    int     `json:"maxContainers"`
	MemUsedBytes     uint64  `json:"memUsedBytes"`
	MemTotalBytes    uint64  `json:"memTotalBytes"`
	MemPct           float64 `json:"memPct"`
	CPUUsedPct       float64 `json:"cpuUsedPct"`
	CPUWatermarkPct  float64 `json:"cpuWatermarkPct"`
}

type cacheEntry struct {
	rep *Report
	at  int64 // unix 秒
}

// Checker 配额检查器。按地区缓存最近统计,避免每请求打 Docker stats(耗时)。
type Checker struct {
	opts Options
	orch docker.Orchestrator
	st   *store.Store

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// New 创建检查器。
func New(opts Options, orch docker.Orchestrator, st *store.Store) *Checker {
	return &Checker{opts: opts, orch: orch, st: st, cache: map[string]cacheEntry{}}
}

// CanCreate 是否允许在指定地区创建新容器。region 为空表示全局。
// ok=false 时 reason 说明原因。
func (c *Checker) CanCreate(ctx context.Context, region string) (ok bool, reason string, rep *Report, err error) {
	rep, err = c.ReportFor(ctx, region)
	if err != nil {
		return false, "", nil, err
	}
	if rep.ActiveContainers >= rep.MaxContainers {
		return false, fmt.Sprintf("容器数量已达上限 %d,请回收过期实例后再试", rep.MaxContainers), rep, nil
	}
	if rep.MemPct > c.opts.MemoryWatermarkPct {
		return false, fmt.Sprintf("宿主内存使用率 %.1f%% 超过水位 %.0f%%", rep.MemPct, c.opts.MemoryWatermarkPct), rep, nil
	}
	if rep.CPUUsedPct > c.opts.CPUWatermarkPct {
		return false, fmt.Sprintf("容器总 CPU 使用率 %.1f%% 超过水位 %.0f%%", rep.CPUUsedPct, c.opts.CPUWatermarkPct), rep, nil
	}
	return true, "", rep, nil
}

// Report 全局资源报告(所有地区)。
func (c *Checker) Report(ctx context.Context) (*Report, error) {
	return c.ReportFor(ctx, "")
}

// ReportFor 指定地区的资源报告。1 秒缓存。
func (c *Checker) ReportFor(ctx context.Context, region string) (*Report, error) {
	now := time.Now().Unix()
	c.mu.Lock()
	if e, ok := c.cache[region]; ok && e.rep != nil && now-e.at < 1 {
		rep := *e.rep
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
		if i.State == "active" && (region == "" || i.Region == region) {
			active++
		}
	}

	// 地区配额优先于全局默认值。
	maxContainers := c.opts.MaxContainers
	regionName := ""
	if region != "" {
		if reg, err := c.st.GetRegion(ctx, region); err == nil {
			maxContainers = reg.EffectiveMaxContainers(c.opts.MaxContainers)
			regionName = reg.Name
		}
	}

	rep := &Report{
		Region:           region,
		RegionName:       regionName,
		ActiveContainers: active,
		MaxContainers:    maxContainers,
		MemTotalBytes:    hostMemTotal(),
		CPUWatermarkPct:  c.opts.CPUWatermarkPct,
	}

	// 地区感知编排:按地区取 stats 与宿主内存;否则退回全局。
	ro, regionAware := c.orch.(docker.RegionOrchestrator)
	if regionAware && region != "" {
		if info, err := ro.HostInfoFor(ctx, region); err == nil && info.MemoryTotalBytes > 0 {
			rep.MemTotalBytes = info.MemoryTotalBytes
		}
	}

	var stats map[string]*docker.ContainerStat
	switch {
	case regionAware && region != "":
		stats, _ = ro.StatsFor(ctx, region) // 失败降级(仅容器数配额仍生效)
	case regionAware:
		// 全局报告:逐地区汇总容器资源与宿主内存,避免用本机内存除全地区用量。
		regions, rerr := c.st.ListRegions(ctx)
		if rerr != nil || len(regions) == 0 {
			stats, _ = c.orch.Stats(ctx)
			break
		}
		stats = map[string]*docker.ContainerStat{}
		var totalMem uint64
		for _, reg := range regions {
			if s, err := ro.StatsFor(ctx, reg.ID); err == nil {
				for name, st := range s {
					stats[name] = st
				}
			}
			if info, err := ro.HostInfoFor(ctx, reg.ID); err == nil {
				totalMem += info.MemoryTotalBytes
			}
		}
		if totalMem > 0 {
			rep.MemTotalBytes = totalMem
		}
	default:
		stats, _ = c.orch.Stats(ctx)
	}
	for _, s := range stats {
		rep.MemUsedBytes += s.MemBytes
		rep.CPUUsedPct += s.CPUPct
	}
	if rep.MemTotalBytes > 0 {
		rep.MemPct = float64(rep.MemUsedBytes) / float64(rep.MemTotalBytes) * 100
	}

	c.mu.Lock()
	c.cache[region] = cacheEntry{rep: rep, at: now}
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
