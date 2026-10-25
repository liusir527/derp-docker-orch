package docker

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"
)

// HostInfo 读取本机资源概况(regiond 上报给控制面用)。
func (d *DockerOrchestrator) HostInfo(ctx context.Context) (HostInfo, error) {
	return HostInfo{MemoryTotalBytes: readMemTotal(), CPUCores: runtime.NumCPU()}, nil
}

// readMemTotal 读取 /proc/meminfo 的 MemTotal(字节),失败返回 0。
func readMemTotal() uint64 {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			kb, _ := strconv.ParseUint(fields[1], 10, 64)
			return kb * 1024
		}
	}
	return 0
}
