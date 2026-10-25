// Package agent 实现 regiond 远程编排代理的协议、服务端与客户端。
// 控制面(platformd)通过它管理其他地区的宿主机,无需暴露 Docker API。
package agent

import (
	"derp-orch/internal/docker"
	"derp-orch/internal/store"
)

// EnsureNodeRequest 创建/确保一个 derper 容器。
type EnsureNodeRequest struct {
	Instance *store.Instance `json:"instance"`
}

// NodeResponse 节点操作结果。
type NodeResponse struct {
	State  docker.ContainerState `json:"state"`
	HostIP string                `json:"hostIP,omitempty"`
}

// StatsResponse 本机全部 derp 容器资源统计(按容器名索引)。
type StatsResponse struct {
	Stats map[string]*docker.ContainerStat `json:"stats"`
}

// InfoResponse 宿主机资源概况。
type InfoResponse struct {
	docker.HostInfo
	DockerOK bool `json:"dockerOk"`
}
