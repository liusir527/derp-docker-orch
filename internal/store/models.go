package store

import "time"

// Instance 一个 DERP 中继实例。临时生命周期,expires_at 到期由 sweeper 清理。
type Instance struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Subdomain     string    `json:"subdomain"`
	RegionID      int       `json:"regionId"`
	RegionCode    string    `json:"regionCode"`
	STUNPort      int       `json:"stunPort"`
	ContainerName string    `json:"containerName"`
	ExpiresAt     time.Time `json:"expiresAt"`
	State         string    `json:"state"`
	CreatedAt     time.Time `json:"createdAt"`
	// HostIP Host 网络模式下容器在宿主可路由的 IP(derper 端口直接可达)。
	// bridge 模式为 Docker 网关 IP,host 模式为 "127.0.0.1"。
	HostIP string `json:"hostIP"`
	// Region 实例所属地区 slug(regions.id)。历史数据为空,启动时回填为本地地区。
	Region string `json:"region"`
}

// Region 一个可部署中继的宿主机/地区。
// AgentURL 为空表示本机 docker.sock;否则通过 regiond 代理远程编排。
type Region struct {
	ID            string    `json:"id"` // slug,如 wh / cd;主键,创建后不可改
	Name          string    `json:"name"`
	BaseDomain    string    `json:"baseDomain"`
	RegionCode    string    `json:"regionCode"`
	AgentURL      string    `json:"agentUrl,omitempty"`
	AgentToken    string    `json:"-"` // 永不外泄,列表接口用 hasAgentToken 表示
	MaxContainers int       `json:"maxContainers"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"createdAt"`
}

// Local 是否为本地地区(无远程代理)。
func (r *Region) Local() bool { return r.AgentURL == "" }

// EffectiveMaxContainers 地区配额上限,0 表示沿用平台默认值。
func (r *Region) EffectiveMaxContainers(def int) int {
	if r.MaxContainers > 0 {
		return r.MaxContainers
	}
	return def
}

// Expired 是否已过期。
func (i *Instance) Expired(now time.Time) bool {
	return !i.ExpiresAt.After(now)
}

// RedeemCode 一个兑换码。一次性消耗,used_at/used_by_instance_id 标记使用记录。
// 预留机制:reserved_token 抢占后 15 分钟内未消费则自动释放(见 RedeemStore)。
type RedeemCode struct {
	ID               int64      `json:"id"`
	Code             string     `json:"code"`
	DurationValue    int        `json:"durationValue"`
	DurationUnit     string     `json:"durationUnit"` // day | month | year
	Disabled         bool       `json:"disabled"`
	UsedByInstanceID *string    `json:"usedByInstanceId,omitempty"`
	UsedAt           *time.Time `json:"usedAt,omitempty"`
	CreatedBy        string     `json:"createdBy"`
	CreatedAt        time.Time  `json:"createdAt"`
	Note             string     `json:"note,omitempty"`
}

// Used 是否已被使用。
func (c *RedeemCode) Used() bool { return c.UsedAt != nil }

// AuditLog 操作审计日志。时间精确到分钟展示。
type AuditLog struct {
	ID         int64     `json:"id"`
	Kind       string    `json:"kind"` // redeem | create_code | delete_instance
	InstanceID string    `json:"instanceId,omitempty"`
	Code       string    `json:"code,omitempty"`
	Username   string    `json:"username,omitempty"`
	Detail     string    `json:"detail,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
}
