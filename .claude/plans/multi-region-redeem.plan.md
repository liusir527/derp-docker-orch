# 多地区（多宿主机）DERP 中继支持

## Summary

当前平台是**单宿主机**模型：一个 `RelayBaseDomain`、一个本地 docker.sock、一份实例表、一个 derpMap 地区。目标是增加第二个地区的服务器（B），并让**一个兑换码池跨地区共享**：用户兑换时自己选地区（华中 wh / 西南 cd），系统在对应宿主机创建中继，返回该地区单地区 derpMap。

B 宿主机不暴露 Docker API，而是运行 `regiond` 轻量代理（复用同一镜像、挂载 B 的 docker.sock），A 上的 `platformd` 用 HTTPS + Token 调用它完成容器编排，并读取 B 的宿主资源用于配额判断。

## 实现状态（2026-10-25）

已完成并验证：store/config/derp/docker/limits 改造，agent 协议与服务端/客户端，region Router，`cmd/regiond`，API（公开地区、admin 地区 CRUD/测试、兑换选地区、实例/审计/统计带地区），前端（兑换页地区选择、管理页地区管理与分地区统计/地区列），部署（Dockerfile 双二进制、`deploy/region-compose.yml`、README 多地区章节）。`go test ./... -race` 全绿；本地冒烟：`/v1/regions`、兑换选地区失败回滚、`regiond` 鉴权均符合预期。

## 基线

| 层 | 现状 |
| --- | --- |
| 兑换 | `redeem_codes` 一码一实例（`used_by_instance_id`），码池全局 |
| 实例 | `instances` 表，`region_id`/`stun_port` 全局唯一，子域名由单个 base domain 派生 |
| 编排 | `docker.Orchestrator` 直连本机 docker.sock，`EnsureNode/RemoveNode/NodeState/Stats` |
| 限额 | 全局 `MAX_CONTAINERS` + 本机 `/proc/meminfo` |
| derpMap | `derp.MapForInstance` 生成单地区 |
| 面板 | 兑换页无地区选择；管理页无地区维度 |

## 设计

### 地区注册表

```go
type Region struct {
    ID           string    // slug，如 wh / cd（主键，不可改）
    Name         string    // 展示名，如 华中 / 西南
    BaseDomain   string    // 该地区中继基础域，如 wh.easylan.site
    RegionCode   string    // derpMap regionCode，如 wh
    AgentURL     string    // 空=本机 docker.sock；否则 https://agent.<domain>
    AgentToken   string    // 访问 regiond 的 Bearer Token
    MaxContainers int      // 0=沿用平台默认 MAX_CONTAINERS
    Enabled      bool
    CreatedAt    time.Time
}
```

- 本地地区由环境变量 `REGION_SLUG / REGION_NAME / REGION_CODE / RELAY_BASE_DOMAIN` 在启动时 seed，已存在则不改（面板可改 Name/MaxContainers，BaseDomain 只影响新实例）。
- `instances` 增加 `region TEXT NOT NULL DEFAULT ''`，启动时把历史空值回填为本地 slug。
- `region_id` / `stun_port` 仍全局唯一：跨宿主机用同一套数字池，天然保证不同宿主上的端口不冲突；新增 `REGION_ID_MAX`（默认 999）防止越界分配。
- 子域名 = `derp-<name>.relay.<region.base_domain>`；`RegionCode` 取地区配置（历史实例仍可从名字推导）。

### 编排路由

```go
// internal/region
type Router struct { st *store.Store; local docker.Orchestrator; localSlug string; clients map[string]*agent.Client }
// 实现 docker.Orchestrator（按 inst.Region 路由）
// 额外实现 docker.RegionOrchestrator:
//   StatsFor(ctx, region) (map[string]*ContainerStat, error)
//   HostInfoFor(ctx, region) (docker.HostInfo, error)
```

- `RegionOrchestrator` 是可选接口，`limits.Checker` 类型断言使用；老测试用假 orchestrator 时自动退化为全局行为。
- `regiond` 代理 API（Bearer Token，`subtle.ConstantTimeCompare`）：
  - `GET /healthz` → `{ok}`
  - `GET /v1/info` → `{memoryTotalBytes, cpuCores, dockerOk}`
  - `POST /v1/nodes` `{instance}` → `EnsureNode` → `{state, hostIP}`
  - `DELETE /v1/nodes/{name}` → `{ok}`
  - `GET /v1/nodes/{name}` → `{state}`
  - `GET /v1/stats` → `{stats}`
- `internal/agent.Client` 同时实现 `docker.Orchestrator` + `HostInfoer`，供 Router 使用；client/server 共用一个 `protocol.go`，测试用 `httptest` 双向验证。

### 限额

- `limits.Checker.CanCreate(ctx, region)` / `Report(ctx)`（全局）/ `ReportFor(ctx, region)`。
- 地区有 `MaxContainers` 覆盖时按地区算；宿主内存/CPU 通过 `HostInfoFor` 取对应宿主。
- 管理面板统计按地区展开。

### 接口

| 方法 | 路径 | 说明 |
| --- | --- | --- |
| GET | `/v1/regions` | 公开：启用地区 `[{id,name,baseDomain}]` |
| POST | `/v1/redeem/redeem` | body 增加 `region`（可空=本地） |
| GET/POST | `/v1/admin/regions` | 列表（token 脱敏）/ 新建 |
| PUT/DELETE | `/v1/admin/regions/{id}` | 修改 / 删除（本地或仍有实例则拒绝） |
| POST | `/v1/admin/regions/{id}/test` | 探测 agent 并返回 info |
| GET | `/v1/admin/stats` | 增加 `regions: []Report` |
| GET | `/v1/admin/instances` | 行增加 `region` / `regionName` |

### 部署

- A（华中）：现有 compose 不变，增加 `REGION_SLUG=wh`、`REGION_NAME=华中`、`REGION_CODE=wh`、`REGION_ID_MAX=999`。
- B（西南）：新增 `deploy/region-compose.yml`，含 `nginx-proxy` + `regiond`（`entrypoint: /usr/local/bin/regiond`），`REGION_AGENT_TOKEN` 必填，端口只绑 `127.0.0.1:8081`，由 B 的 BT-nginx 反代成 `https://agent.cd.easylan.site`。
- 镜像同时包含 `platformd` 与 `regiond` 两个二进制。

## 任务

1. store：Region 模型、v5 迁移（regions 表 + instances.region + 索引）、region CRUD、回填、分配上限。
2. config：`REGION_SLUG/NAME/CODE/ID_MAX`；`derp.Subdomain`。
3. docker：`HostInfo`/`HostInfoer`、`RegionOrchestrator` 接口。
4. limits：按地区统计与判定，1s 缓存按地区 key。
5. agent：协议、Server、Client + 测试。
6. region：Router + 测试。
7. cmd/regiond + Dockerfile + region compose。
8. api：regions handler、redeem/instances/admin 改造。
9. main.go 接线（seed 本地地区、回填、Router）。
10. 前端：兑换页地区选择、管理页地区列 + 地区管理卡片 + 分地区统计。
11. README（架构图、env 表、B 部署步骤、API）。
12. `gofmt`、`go vet`、`go test ./... -race`。

## 验收

- 单地区（不配置第二个地区）行为与现状一致，旧码可继续兑换/找回。
- 新建 cd 地区后：兑换页出现地区选择；选 cd 时实例落在 B 宿主、derpMap 的 hostname 为 `*.relay.cd.easylan.site`；A 面板能看到两地实例与资源。
- 删除有实例的地区被拒绝；agent 不可达时兑换返回明确错误并回滚码状态。
- `go test ./... -race` 全绿。
