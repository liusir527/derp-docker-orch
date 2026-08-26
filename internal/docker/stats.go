package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

// statsSample 一次容器 stats 采样(cpu_stats/memory_stats 字段)。
type statsSample struct {
	CPUStats    cpuStat `json:"cpu_stats"`
	PreCPUStats cpuStat `json:"precpu_stats"`
	MemoryStats struct {
		Usage uint64 `json:"usage"`
	} `json:"memory_stats"`
}

type cpuStat struct {
	CPUUsage struct {
		TotalUsage uint64 `json:"total_usage"`
	} `json:"cpu_usage"`
	SystemCPUUsage uint64 `json:"system_cpu_usage"`
}

// Stats 汇总本平台全部容器的资源统计(CPU% + 内存字节)。
// CPU% 用两次采样差值计算;与 docker stats 输出口径一致。
func (d *DockerOrchestrator) Stats(ctx context.Context) (map[string]*ContainerStat, error) {
	names, err := d.orchContainerNames(ctx)
	if err != nil {
		return nil, err
	}
	if len(names) == 0 {
		return map[string]*ContainerStat{}, nil
	}
	// 第一次采样。
	first, err := d.sampleAll(ctx, names)
	if err != nil {
		return nil, err
	}
	time.Sleep(200 * time.Millisecond)
	// 第二次采样。
	second, err := d.sampleAll(ctx, names)
	if err != nil {
		return nil, err
	}

	out := make(map[string]*ContainerStat, len(names))
	for _, name := range names {
		a, okA := first[name]
		b, okB := second[name]
		if !okA || !okB {
			continue
		}
		cpu := 0.0
		sysDelta := b.CPUStats.SystemCPUUsage - a.CPUStats.SystemCPUUsage
		userDelta := b.CPUStats.CPUUsage.TotalUsage - a.CPUStats.CPUUsage.TotalUsage
		if sysDelta > 0 {
			cpu = float64(userDelta) / float64(sysDelta) * 100.0
		}
		out[name] = &ContainerStat{
			Container: name,
			CPUPct:    cpu,
			MemBytes:  b.MemoryStats.Usage,
		}
	}
	return out, nil
}

// sampleAll 拉取全部容器当前 stats,按容器名索引。
func (d *DockerOrchestrator) sampleAll(ctx context.Context, names []string) (map[string]statsSample, error) {
	out := make(map[string]statsSample, len(names))
	for _, name := range names {
		resp, err := d.cli.ContainerStatsOneShot(ctx, name)
		if err != nil {
			continue // 容器刚被删/停,跳过
		}
		defer resp.Body.Close()
		var s statsSample
		if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
			continue
		}
		out[name] = s
	}
	return out, nil
}

// orchContainerNames 列出本平台全部容器名(app=derp-orch)。
func (d *DockerOrchestrator) orchContainerNames(ctx context.Context) ([]string, error) {
	list, err := d.cli.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "app=derp-orch")),
	})
	if err != nil {
		return nil, fmt.Errorf("列出容器: %w", err)
	}
	names := make([]string, 0, len(list))
	for _, c := range list {
		if len(c.Names) > 0 {
			names = append(names, trimSlash(c.Names[0]))
		}
	}
	return names, nil
}

func trimSlash(s string) string {
	if len(s) > 0 && s[0] == '/' {
		return s[1:]
	}
	return s
}
