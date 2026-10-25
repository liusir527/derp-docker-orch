// Package region 按实例所属地区路由编排请求:本地 docker.sock 或远程 regiond。
package region

import (
	"context"
	"fmt"
	"log"
	"sync"

	"derp-orch/internal/agent"
	"derp-orch/internal/docker"
	"derp-orch/internal/store"
)

// Router 多地区编排路由。实现 docker.Orchestrator 与 docker.RegionOrchestrator。
type Router struct {
	st        *store.Store
	local     docker.Orchestrator
	localSlug string

	mu      sync.Mutex
	clients map[string]*agent.Client // key: 地区 slug
	fps     map[string]string        // 地区 slug -> url+token 指纹,配置变更时重建客户端
}

// New 创建路由。localSlug 为空时使用 "local"。
func New(st *store.Store, local docker.Orchestrator, localSlug string) *Router {
	if localSlug == "" {
		localSlug = "local"
	}
	return &Router{
		st:        st,
		local:     local,
		localSlug: localSlug,
		clients:   map[string]*agent.Client{},
		fps:       map[string]string{},
	}
}

// EnsureNode 在实例所属地区创建/确保容器。
func (r *Router) EnsureNode(ctx context.Context, i *store.Instance) error {
	o, _, err := r.orchFor(ctx, i.Region)
	if err != nil {
		return err
	}
	return o.EnsureNode(ctx, i)
}

// RemoveNode 在实例所属地区删除容器。
func (r *Router) RemoveNode(ctx context.Context, i *store.Instance) error {
	o, _, err := r.orchFor(ctx, i.Region)
	if err != nil {
		return err
	}
	return o.RemoveNode(ctx, i)
}

// NodeState 查询实例所属地区的容器状态。
func (r *Router) NodeState(ctx context.Context, i *store.Instance) (docker.ContainerState, error) {
	o, _, err := r.orchFor(ctx, i.Region)
	if err != nil {
		return docker.StateMissing, err
	}
	return o.NodeState(ctx, i)
}

// Stats 汇总全部地区的容器统计。单地区/未初始化时退回本地。
func (r *Router) Stats(ctx context.Context) (map[string]*docker.ContainerStat, error) {
	regions, err := r.st.ListRegions(ctx)
	if err != nil {
		return nil, fmt.Errorf("列出地区: %w", err)
	}
	if len(regions) == 0 {
		return r.local.Stats(ctx)
	}
	out := map[string]*docker.ContainerStat{}
	for _, reg := range regions {
		stats, err := r.StatsFor(ctx, reg.ID)
		if err != nil {
			log.Printf("[region] 地区 %s 资源统计失败: %v", reg.ID, err)
			continue
		}
		for name, s := range stats {
			out[name] = s
		}
	}
	return out, nil
}

// StatsFor 单个地区的容器统计。
func (r *Router) StatsFor(ctx context.Context, slug string) (map[string]*docker.ContainerStat, error) {
	o, _, err := r.orchFor(ctx, slug)
	if err != nil {
		return nil, err
	}
	return o.Stats(ctx)
}

// HostInfoFor 单个地区的宿主资源。本地读 /proc/meminfo,远程问 regiond。
func (r *Router) HostInfoFor(ctx context.Context, slug string) (docker.HostInfo, error) {
	o, reg, err := r.orchFor(ctx, slug)
	if err != nil {
		return docker.HostInfo{}, err
	}
	if reg.Local() {
		if hi, ok := r.local.(docker.HostInfoer); ok {
			return hi.HostInfo(ctx)
		}
		return docker.HostInfo{}, nil
	}
	hi, ok := o.(docker.HostInfoer)
	if !ok {
		return docker.HostInfo{}, nil
	}
	return hi.HostInfo(ctx)
}

// AgentInfo 探测指定地区的代理(管理面板"测试"按钮)。本地地区返回本地宿主信息。
func (r *Router) AgentInfo(ctx context.Context, slug string) (*agent.InfoResponse, error) {
	_, reg, err := r.orchFor(ctx, slug)
	if err != nil {
		return nil, err
	}
	out := &agent.InfoResponse{}
	if reg.Local() {
		if hi, ok := r.local.(docker.HostInfoer); ok {
			out.HostInfo, _ = hi.HostInfo(ctx)
		}
		out.DockerOK = localPingOK(ctx, r.local)
		return out, nil
	}
	return r.client(reg).Info(ctx)
}

func localPingOK(ctx context.Context, o docker.Orchestrator) bool {
	if p, ok := o.(interface{ Ping(context.Context) error }); ok {
		return p.Ping(ctx) == nil
	}
	return true
}

func (r *Router) orchFor(ctx context.Context, slug string) (docker.Orchestrator, *store.Region, error) {
	reg, err := r.region(ctx, slug)
	if err != nil {
		return nil, nil, err
	}
	if reg.Local() {
		return r.local, reg, nil
	}
	return r.client(reg), reg, nil
}

func (r *Router) region(ctx context.Context, slug string) (*store.Region, error) {
	if slug == "" {
		slug = r.localSlug
	}
	reg, err := r.st.GetRegion(ctx, slug)
	if err != nil {
		return nil, fmt.Errorf("地区 %q 不可用: %w", slug, err)
	}
	return reg, nil
}

// client 按当前地区配置返回(必要时重建)代理客户端。
func (r *Router) client(reg *store.Region) *agent.Client {
	fp := reg.AgentURL + "\x00" + reg.AgentToken
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.clients[reg.ID]; ok && r.fps[reg.ID] == fp {
		return c
	}
	c := agent.NewClient(reg.AgentURL, reg.AgentToken)
	r.clients[reg.ID] = c
	r.fps[reg.ID] = fp
	return c
}
