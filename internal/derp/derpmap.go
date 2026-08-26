package derp

import (
	"encoding/json"
	"fmt"

	"derp-orch/internal/store"
)

// 以下结构体与 Tailscale KB 1118「Custom DERP Servers」的 ACL policy 格式逐字段一致,
// 租户需能直接把输出粘贴进管理台 ACL 文件。

// derpMap 顶层映射。
type derpMap struct {
	OmitDefaultRegions bool               `json:"omitDefaultRegions"`
	Regions            map[string]*region `json:"regions"`
}

type region struct {
	RegionID   int    `json:"regionID"`
	RegionCode string `json:"regionCode"`
	RegionName string `json:"regionName"`
	Nodes      []node `json:"nodes"`
}

type node struct {
	Name     string `json:"name"`
	RegionID int    `json:"regionID"`
	HostName string `json:"hostName"`
	DERPPort int    `json:"derpPort"`
	STUNPort int    `json:"stunPort"`
}

// MapForInstance 生成实例的 derpMap JSON(可直接粘贴进 ACL)。
// derpPort 非 443 时(例如 1443)客户端需将 DERP 连到自定义端口。
func MapForInstance(i *store.Instance, derpPort int) ([]byte, error) {
	if derpPort == 0 {
		derpPort = 443
	}
	m := derpMap{
		OmitDefaultRegions: false,
		Regions: map[string]*region{
			fmt.Sprintf("%d", i.RegionID): {
				RegionID:   i.RegionID,
				RegionCode: i.RegionCode,
				RegionName: i.Name,
				Nodes: []node{{
					Name:     "1",
					RegionID: i.RegionID,
					HostName: i.Subdomain,
					DERPPort: derpPort,
					STUNPort: i.STUNPort,
				}},
			},
		},
	}
	out, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("序列化 derpMap: %w", err)
	}
	return out, nil
}
