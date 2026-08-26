// Package docker 编排 derper 容器。
// Orchestrator 接口是未来多节点池化的唯一扩展点(计划 NOT Building: 调度不实现)。
package docker

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"derp-orch/internal/store"
)

// ContainerState 容器状态。
type ContainerState string

const (
	StateRunning ContainerState = "running"
	StateExited  ContainerState = "exited"
	StateMissing ContainerState = "missing"
)

// State 实例运行时状态。
type State struct {
	Container ContainerState `json:"container"`
}

// Orchestrator 容器编排抽象。
type Orchestrator interface {
	EnsureNode(ctx context.Context, i *store.Instance) error
	RemoveNode(ctx context.Context, i *store.Instance) error
	NodeState(ctx context.Context, i *store.Instance) (ContainerState, error)
}

// Options Docker 编排器配置。
type Options struct {
	Host      string // Docker Engine API 地址
	Image     string // derper 镜像
	Network   string // DEPRECATED: 改用默认 bridge 网,保留字段保持向后兼容
	CertsDir  string // DEPRECATED: certmode=none 时不挂载,保留字段保持向后兼容
	VerifyURL func(i *store.Instance) string // DEPRECATED: 新镜像无 --verify-client-url,保留字段保持向后兼容
}

// DockerOrchestrator 基于 Docker Engine API 的实现。
type DockerOrchestrator struct {
	cli *client.Client
	op  Options
	store *store.Store
}

// New 创建编排器。
func New(op Options, store *store.Store) (*DockerOrchestrator, error) {
	cli, err := client.NewClientWithOpts(
		client.WithHost(op.Host),
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("创建 Docker 客户端: %w", err)
	}
	return &DockerOrchestrator{cli: cli, op: op}, nil
}

// Ping 探测 Docker daemon 可达性。
func (d *DockerOrchestrator) Ping(ctx context.Context) error {
	_, err := d.cli.Ping(ctx)
	return err
}

// EnsureNode 幂等确保容器存在且运行。配置等价则跳过,否则重建。
func (d *DockerOrchestrator) EnsureNode(ctx context.Context, i *store.Instance) error {
	name := i.ContainerName
	exists, running, err := d.find(ctx, name)
	if err != nil {
		return err
	}
	cmd := d.derperCmd(i)
	if exists && running && d.configMatches(ctx, name, cmd) {
		return nil // 等价,跳过。
	}
	if exists {
		log.Printf("[orch] 重建容器 %s(配置变更或已停止)", name)
		if err := d.cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("移除旧容器 %s: %w", name, err)
		}
	}
	if err := d.createAndStart(ctx, i, cmd); err != nil {
		return err
	}
	log.Printf("[orch] 容器 %s 已就绪(实例 %s)", name, i.ID)
	return nil
}

// RemoveNode 强制移除容器。容器不存在视为成功(幂等)。
func (d *DockerOrchestrator) RemoveNode(ctx context.Context, i *store.Instance) error {
	err := d.cli.ContainerRemove(ctx, i.ContainerName, container.RemoveOptions{Force: true})
	if err != nil && !client.IsErrNotFound(err) {
		return fmt.Errorf("移除容器 %s: %w", i.ContainerName, err)
	}
	log.Printf("[orch] 容器 %s 已移除", i.ContainerName)
	return nil
}

// NodeState 查询容器状态。
func (d *DockerOrchestrator) NodeState(ctx context.Context, i *store.Instance) (ContainerState, error) {
	_, running, err := d.find(ctx, i.ContainerName)
	if err != nil {
		return StateMissing, err
	}
	if !running {
		return StateMissing, nil
	}
	return StateRunning, nil
}

func (d *DockerOrchestrator) derperCmd(i *store.Instance) []string {
	return []string{
		"/app/derper",
		"--hostname", i.Subdomain,
		"--certmode", "none",
		"-a", ":80",
		"--stun", "true",
		"--stun-port", "3478",
		"--http-port", "80",
		"--verify-clients", "false",
	}
}

func (d *DockerOrchestrator) createAndStart(ctx context.Context, i *store.Instance, cmd []string) error {
	cfg := &container.Config{
		Image: d.op.Image,
		Cmd:   cmd,
		Env: []string{
			"VIRTUAL_HOST=" + i.Subdomain,
			"VIRTUAL_PORT=80",
			"VIRTUAL_PROTO=http",
			"DERP_DOMAIN=" + i.Subdomain,
		},
		Labels: map[string]string{
			"app":      "derp-orch",
			"instance": i.ID,
		},
		ExposedPorts: nat.PortSet{
			"80/tcp": {},
		},
	}
	hostCfg := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
		PortBindings:  natPortMap(i.STUNPort),
	}
	// 挂到编排网络(与 nginx-proxy 同网),docker-gen 才能发现容器生成 vhost。
	netCfg := &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			d.op.Network: {},
		},
	}
	if _, err := d.cli.ContainerCreate(ctx, cfg, hostCfg, netCfg, nil, i.ContainerName); err != nil {
		return fmt.Errorf("创建容器 %s: %w", i.ContainerName, err)
	}
	if err := d.cli.ContainerStart(ctx, i.ContainerName, container.StartOptions{}); err != nil {
		return fmt.Errorf("启动容器 %s: %w", i.ContainerName, err)
	}
	// 保留 HostIP 写入保持向后兼容(nginx-proxy 自动发现,不再消费此字段)。
	hostIP, _ := d.containerHostIP(ctx, i.ContainerName)
	i.HostIP = hostIP
	return nil
}

// find 查询容器是否存在与运行中。
func (d *DockerOrchestrator) find(ctx context.Context, name string) (exists, running bool, err error) {
	list, err := d.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("name", "^/"+name+"$")),
	})
	if err != nil {
		return false, false, fmt.Errorf("查询容器 %s: %w", name, err)
	}
	for _, c := range list {
		if strings.TrimPrefix(c.Names[0], "/") == name {
			return true, c.State == "running", nil
		}
	}
	return false, false, nil
}

// configMatches 校验已存在容器的镜像与启动命令是否等价。
func (d *DockerOrchestrator) configMatches(ctx context.Context, name string, cmd []string) bool {
	insp, err := d.cli.ContainerInspect(ctx, name)
	if err != nil {
		return false
	}
	if insp.Config == nil {
		return false
	}
	if insp.Config.Image != d.op.Image || !stringSliceEq(insp.Config.Cmd, cmd) {
		return false
	}
	return true
}

func stringSliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// containerHostIP 查询容器在 orch-net 中的 IP。
func (d *DockerOrchestrator) containerHostIP(ctx context.Context, name string) (string, error) {
	resp, err := d.cli.ContainerInspect(ctx, name)
	if err != nil {
		return "", err
	}
	if n := resp.NetworkSettings.Networks[d.op.Network]; n != nil {
		return n.IPAddress, nil
	}
	return "", fmt.Errorf("容器 %s 不在网络 %s", name, d.op.Network)
}

func natPortMap(hostSTUN int) nat.PortMap {
	return nat.PortMap{
		nat.Port("3478/udp"): {{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", hostSTUN)}},
	}
}

