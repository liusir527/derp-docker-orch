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
