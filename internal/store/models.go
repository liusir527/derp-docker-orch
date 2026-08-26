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
}

// Expired 是否已过期。
func (i *Instance) Expired(now time.Time) bool {
	return !i.ExpiresAt.After(now)
}

// RedeemCode 一个兑换码。一次性消耗,used_at/used_by_instance_id 标记使用记录。
// 预留机制:reserved_token 抢占后 15 分钟内未消费则自动释放(见 RedeemStore)。
type RedeemCode struct {
	ID                int64      `json:"id"`
	Code              string     `json:"code"`
	DurationValue     int        `json:"durationValue"`
	DurationUnit      string     `json:"durationUnit"` // day | month | year
	Disabled          bool       `json:"disabled"`
	UsedByInstanceID  *string    `json:"usedByInstanceId,omitempty"`
	UsedAt            *time.Time `json:"usedAt,omitempty"`
	CreatedBy         string     `json:"createdBy"`
	CreatedAt         time.Time  `json:"createdAt"`
	Note              string     `json:"note,omitempty"`
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
