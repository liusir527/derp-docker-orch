package derp

import (
	"encoding/json"
	"testing"
	"time"

	"derp-orch/internal/store"
)

func TestMapForInstance_MatchesKB1118(t *testing.T) {
	inst := &store.Instance{
		ID:         "derp-20260812-x7k2",
		Name:       "derp-20260812-x7k2",
		Subdomain:  "derp-20260812-x7k2.relay.wh.easylan.site",
		RegionID:   900,
		RegionCode: "260812",
		STUNPort:   3479,
		ExpiresAt:  time.Now().Add(time.Hour),
	}
	out, err := MapForInstance(inst, 443)
	if err != nil {
		t.Fatalf("MapForInstance: %v", err)
	}

	// 反序列化逐字段断言,与 KB 1118 结构一致。
	var m derpMap
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if m.OmitDefaultRegions != false {
		t.Errorf("omitDefaultRegions 应为 false,实际 %v", m.OmitDefaultRegions)
	}
	reg, ok := m.Regions["900"]
	if !ok {
		t.Fatalf("缺少 region key \"900\"")
	}
	if reg.RegionID != 900 || reg.RegionCode != "260812" || reg.RegionName != inst.Name {
		t.Errorf("region 字段不匹配: %+v", reg)
	}
	if len(reg.Nodes) != 1 {
		t.Fatalf("nodes 应含 1 个节点,实际 %d", len(reg.Nodes))
	}
	n := reg.Nodes[0]
	if n.Name != "1" || n.RegionID != 900 || n.HostName != inst.Subdomain ||
		n.DERPPort != 443 || n.STUNPort != 3479 {
		t.Errorf("node 字段不匹配: %+v", n)
	}
}
