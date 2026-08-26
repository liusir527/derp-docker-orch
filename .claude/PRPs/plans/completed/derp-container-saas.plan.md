# Plan: DERP 容器 SaaS 平台(临时实例 + SNI 分流 + Web 界面)

## Summary

多租户 DERP 中继 SaaS:Go 平台(platformd)提供最基础 Web 界面 — 创建 derp 实例 → 获取 derpmap JSON → 实例在有效期内运行容器,过期自动删除。域名由预配置 base 派生:`wh.easylan.site` → `derp-<日期>-<随机>.relay.wh.easylan.site`。所有 derper 容器持有同一泛域名证书(`*.relay.wh.easylan.site`),nginx `ssl_preread` 按 SNI 把 TCP:443 分流到对应实例容器。平台提供 per-instance verify URL 做客户端准入(按实例有效期判断)。

## User Story

As a Tailscale 用户(官方 tailscale.com 控制平面),
I want 在 Web 上创建一个临时 DERP 中继、复制 derpmap 粘贴进 ACL policy,有效期内容器运行、过期自动清理,
So that 我的 tailnet 立即获得专属中继,无需自建基础设施,到期自动回收。

## Problem → Solution

现状:用户手工跑 `build-container.sh` 起单个 derper,手工维护证书/端口/derpmap,无过期回收、无 Web 交互 → 目标:一键创建临时 DERP 实例,Web 展示 derpmap,TTL 到期自动删容器、释放端口、摘除 SNI 路由。

## Metadata

- **Complexity**: Large
- **Source PRD**: N/A(自由文本,含两轮需求澄清)
- **PRD Phase**: N/A(standalone)
- **Estimated Files**: 19(Go 源码约 13 + 部署 3 + 文档 2 + 修改 1)
- **语言**: 平台本体 Go 1.22+;部署 shell;nginx 配置;Web 为内嵌单页 HTML/JS(无构建步骤)

---

## UX Design

### Before(用户现状)

```
┌────────────────────────────────────────────────┐
│ 手工执行 build-container.sh                    │
│  - 手动改环境变量、手动拼 derpmap JSON          │
│  - 无过期概念,容器永久跑,资源永不回收          │
│  - 手动管理证书/nginx/端口                     │
└────────────────────────────────────────────────┘
```

### After

```
┌────────────────────────────────────────────────┐
│ Web 页面(platformd 内嵌单页)                    │
│                                                │
│  ① 填写名称 + 选择有效期(默认 24h)→ 创建       │
│  ② 页面显示:                                   │
│     - 子域名 derp-20260812-x7k2.relay.wh.easylan.site │
│     - derpmap JSON(一键复制,粘贴进 ACL)         │
│     - 剩余有效期倒计时 + 容器运行状态           │
│  ③ 到期: 平台后台自动删除容器/释放端口/摘除路由 │
│  ④ 手动删除按钮(立即回收)                      │
└────────────────────────────────────────────────┘
```

### Interaction Changes

| Touchpoint | Before | After | Notes |
|---|---|---|---|
| 创建中继 | 手工 docker run | Web 表单创建 | 页面自动刷新状态 |
| derpmap 获取 | 手工拼 JSON | 页面一键复制 | 与 ACL 格式兼容 |
| 有效期 | 无 | TTL 到期自动清理 | 后台 sweeper 每分钟扫描 |
| 客户端准入 | 无 | derper → 平台 per-instance verify | 按实例有效期拒绝过期连接 |
| 证书 | 手工/挂载 | 共享泛域名证书,续期重启容器 | 所有容器同证书 |

---

## Mandatory Reading

| Priority | File | Lines | Why |
|---|---|---|---|
| P0 (critical) | `build-container.sh` | 全部 | 既有 derper 启动参数事实来源,必须镜像其 flag 组合 |
| P1 (important) | 本计划「Patterns to Mirror」 | 全部 | derper 容器参数唯一代码库事实 |

> 代码库仅此一个文件,Go 工程为全新搭建,无既有 Go 约定可循;下述模式即本项目新约定。

## External Documentation

| Topic | Source | Key Takeaway |
|---|---|---|
| ACL derpMap 格式 | [tailscale.com/kb/1118 custom-derp-servers](https://tailscale.com/kb/1118/custom-derp-servers) | `derpMap` 顶层字段;regionID **900–999 保留自定义**;节点字段 `name/regionID/hostName/derpPort/stunPort`;改 ACL 后需 `tailscale up` 重启;`netcheck` 验证 |
| 镜像与补丁行为 | [github.com/DeepFal/tailscale-derp](https://github.com/DeepFal/tailscale-derp) | 补丁仅移除 `cmd/derper/cert.go` hostname 强校验;`-certmode manual -certdir /ssl`;自动同步上游版本 |
| verify 协议 | [headscale issue #1953](https://github.com/juanfont/headscale/issues/1953) + [pkg.go.dev derpserver](https://pkg.go.dev/tailscale.com@v1.98.5/derp/derpserver) | derper POST `{"NodePublic","Source"}` → 期望 `{"Allow":bool}`;默认 fail-open |
| SNI 路由 | nginx stream `ssl_preread` | TCP 层按 SNI 分流,不终止 TLS |
| TLS 证书匹配 | [forum.tailscale.com custom-derp TLS](https://forum.tailscale.com/t/custom-derp-tls-unsupported-certificate/1134) | 证书 SAN 需匹配 derpMap hostName;`*.relay.wh.easylan.site` 覆盖所有实例子域名 |

### 关键研究结论

- **verify 默认 fail-open**: derper 在 verify 不可达时放行。平台与容器同 docker network + restart policy 缓解
- **regionID 900–999 空间有限**: 全局原子分配,过期实例回收后可复用(仅空间耗尽时);并发实例上限 100
- **STUN 端口每实例独占**: 宿主端口 3478+i 映射进容器 3478;derpMap `stunPort` 填宿主端口
- **per-instance verify**: `--verify-client-url=http://platformd:8080/verify/<instanceID>` 每个容器指向自己的路径,verify 按实例查有效期 — 比全局 allow 精确,无需解析来源 IP

---

## Patterns to Mirror

### DERP_CONTAINER_INVOCATION
```sh
# SOURCE: build-container.sh(全部)— 平台编排生成与此等价的 docker run
docker run -d \
  --name derp-<instanceID> \
  --restart unless-stopped \
  -p <stunHostPort>:3478/udp \
  deepfal/tailscale-derp:latest \
  ./derper \
  -hostname <derp-xxx.relay.wh.easylan.site> \
  -a :36666 \
  -certmode manual \
  -certdir /ssl \
  -stun \
  --verify-clients=false \
  --verify-client-url=http://platformd:8080/verify/<instanceID>
# 注意: TCP 不映射宿主端口 — 443 入口只走 nginx SNI 分流,容器只在 orch-net 内部
```

### VERIFY_REQUEST_RESPONSE
```json
// SOURCE: headscale issue #1953 — derper 调 verify 的 wire 协议
// 请求 (POST, JSON body):
{ "NodePublic": "clientKey", "Source": "clientIP" }
// 响应 (200, JSON body):
{ "Allow": true }
```

### DERPMAP_ACL_FORMAT
```json
// SOURCE: KB 1118 — 用户粘贴进 ACL policy(顶层字段 camelCase)
"derpMap": {
  "omitDefaultRegions": false,
  "regions": {
    "900": {
      "regionID": 900,
      "regionCode": "260812",
      "regionName": "derp-20260812-x7k2",
      "nodes": [
        {
          "name": "1",
          "regionID": 900,
          "hostName": "derp-20260812-x7k2.relay.wh.easylan.site",
          "derpPort": 443,
          "stunPort": 3478
        }
      ]
    }
  }
}
```

### INSTANCE_NAME_FORMAT
```
// SOURCE: 用户需求「derp-xxx 不重复,与日期有关」— 命名规范
derp-<YYYYMMDD>-<4位随机小写字母数字>   例: derp-20260812-x7k2
- 日期段保证"与日期有关";随机段保证同日内唯一
- DB 唯一约束兜底(冲突重试,最多 10 次)
```

---

## Architecture

### 单机 MVP 拓扑

```
                    ┌────────────────────────────── 宿主 ─────────────────────────────┐
客户端 ──TCP:443──▶ │ nginx (stream, ssl_preread)                                      │
客户端 ──UDP:3478+i▶ │   ├─ SNI derp-20260812-x7k2.relay.wh.easylan.site ─▶ derp-<id> :36666 │
                    │   └─ SNI derp-...            ─▶ 其他实例容器                   │
                    │                                                                  │
                    │ platformd (Go 单二进制,内嵌 Web 单页)                            │
                    │   ├─ GET  /            → Web 页面(创建/状态/复制 derpmap)       │
                    │   ├─ POST /v1/instances → 命名+分配 regionID/stunPort+起容器     │
                    │   ├─ DELETE /v1/instances/{id} → 立即回收                      │
                    │   ├─ POST /verify/{instanceID} → per-instance 准入(查有效期)     │
                    │   ├─ sweeper goroutine: 每分钟扫过期实例 → 删容器/释放/更新 nginx │
                    │   ├─ Docker API 编排容器 + nginx 配置渲染                        │
                    │   └─ SQLite (modernc.org/sqlite, 无 CGO)                        │
                    │                                                                  │
                    │ 共享只读卷: /etc/derp-certs(泛域名证书 *.relay.wh.easylan.site)  │
                    │ DNS: *.relay.wh.easylan.site A → 宿主 IP                        │
                    └──────────────────────────────────────────────────────────────────┘
```

### 域名派生规则

```
配置: RELAY_BASE_DOMAIN=wh.easylan.site
实例子域名 = derp-<name>.relay.<base> = derp-20260812-x7k2.relay.wh.easylan.site
通配证书  = *.relay.wh.easylan.site(LE DNS-01,renew-hook 重启 derper 容器)
```

### 设计决策

- **实例模型(临时,非持久租户)**: `instances` 表,TLL 到期 sweeper 清理;资源(regionID/stunPort/子域名)回收可复用
- **SNI 路由 nginx stream `ssl_preread`(TCP 透传)**: derper 自持泛域名证书,nginx 不终止 TLS
- **共享泛域名证书**: 单卷只读挂载所有容器 `-certdir /ssl`(用户明确要求:所有 derp 容器持有相同泛域名证书)
- **per-instance verify URL**: `--verify-client-url=.../verify/<instanceID>`,容器按实例鉴权(活跃且未过期 → Allow)
- **命名**: `derp-<YYYYMMDD>-<rand4>`,随机段 + DB UNIQUE(subdomain)兜底,冲突重试
- **Web 单页**: 内嵌 Go 二进制(embed),无构建步骤、无框架;Admin API key 由浏览器 localStorage 保存,请求带 Bearer 头
- **编排走 Docker Engine API**: 查询/重建/清理幂等;Orchestrator 接口预留多节点
- **SQLite 纯 Go**: 单静态二进制,无 CGO

### 关键约束

1. regionID 全局唯一(900 起),stunPort 宿主唯一(3478 起),均原子分配、删除后回收复用
2. 容器名 = `derp-<instanceID>`,标签 `app=derp-orch,instance=<id>`;nginx 配置全量生成 + `nginx -t` 后 reload,失败回滚
3. derpMap 与 KB 1118 逐字段一致;derpPort 恒 443
4. 过期判定统一在 verify 与 sweeper: `now >= expires_at` 即失效
5. 子域名唯一 = 实例名唯一(DB UNIQUE 约束)

---

## Files to Change

| File | Action | Justification |
|---|---|---|
| `go.mod` | CREATE | Go 工程声明 |
| `cmd/platformd/main.go` | CREATE | 配置加载、DB 迁移、sweeper 启动、HTTP server |
| `internal/config/config.go` | CREATE | 环境变量: `RELAY_BASE_DOMAIN`(wh.easylan.site)、`DEFAULT_TTL_HOURS`(24)、`REGION_ID_BASE`(900)、`STUN_PORT_BASE`(3478)、`DOCKER_HOST`、`ADMIN_API_KEY`(必填)、`CERTS_DIR` |
| `internal/store/store.go` | CREATE | SQLite 连接(WAL+busy_timeout)、迁移(PRAGMA user_version) |
| `internal/store/models.go` | CREATE | Instance 结构体(id, name, subdomain, region_id, region_code, stun_port, container_name, expires_at, state, created_at) |
| `internal/store/instance_store.go` | CREATE | 实例 CRUD + regionID/stunPort 原子分配与回收 |
| `internal/derp/namegen.go` | CREATE | `derp-<YYYYMMDD>-<rand4>` 生成器 + 唯一性重试 |
| `internal/derp/derpmap.go` | CREATE | derpMap JSON 生成(KB 1118 格式) |
| `internal/docker/orchestrator.go` | CREATE | 容器 create/start/remove 幂等;Orchestrator 接口 |
| `internal/nginx/proxy.go` | CREATE | stream 配置渲染 + nginx -t + reload(失败回滚) |
| `internal/verify/verify.go` | CREATE | POST /verify/{instanceID} 按有效期判断 Allow |
| `internal/lifecycle/sweeper.go` | CREATE | 每分钟扫描: 过期实例 → 删容器 → 释放资源 → 更新 nginx |
| `internal/api/router.go` | CREATE | 路由、API key 中间件、`{ok,data,error}` 响应包裹 |
| `internal/api/instances.go` | CREATE | 创建/删除/状态 handler |
| `internal/api/web.go` | CREATE | 内嵌单页 HTML(`//go:embed web/index.html`) |
| `internal/api/web/index.html` | CREATE | 单文件页面: 创建表单、derpmap 复制、倒计时、状态轮询、删除按钮 |
| `deploy/docker-compose.yml` | CREATE | platformd + nginx + 证书卷 + sqlite 卷 + orch-net |
| `deploy/nginx/stream.conf.tmpl` | CREATE | ssl_preread 路由模板 |
| `deploy/certs/issue-wildcard.sh` | CREATE | certbot DNS-01 `*.relay.wh.easylan.site`,renew-hook 重启 derper 容器 |
| `README.md` | CREATE | 部署、证书、租户使用指南 |
| `build-container.sh` | UPDATE | 标注 deprecated,指向平台 |

## NOT Building

- 计费/订阅/多用户登录(单一 Admin key;页面受 key 保护)
- 实例持久化/续期 API(过期即删;续期 = 重建新实例,MVP 不做)
- 多宿主节点池/agent(Orchestrator 接口抽象,不实现调度)
- 自定义 tailscale 客户端分发、官方控制平面接入
- 多证书/租户独立证书(用户明确要求共享泛域名证书)

---

## Step-by-Step Tasks

### Task 1: 工程骨架与配置
- **ACTION**: 初始化 Go module、目录结构、配置加载
- **IMPLEMENT**: `go mod init github.com/<user>/derp-orch`;`internal/config` 环境变量: `RELAY_BASE_DOMAIN`(必填)、`DEFAULT_TTL_HOURS`(默认 24)、`REGION_ID_BASE`(默认 900)、`STUN_PORT_BASE`(默认 3478)、`DOCKER_HOST`(默认 unix:///var/run/docker.sock)、`ADMIN_API_KEY`(必填,缺失启动 panic)、`CERTS_DIR`(默认 /etc/derp-certs)
- **IMPORTS**: 仅标准库
- **GOTCHA**: `RELAY_BASE_DOMAIN` 与 `ADMIN_API_KEY` 缺失即启动失败,防裸奔(common/security.md)
- **VALIDATE**: `go build ./...`;空 `ADMIN_API_KEY` 启动报错

### Task 2: 存储层(SQLite)
- **ACTION**: schema + 迁移 + 实例 CRUD + 资源分配/回收
- **IMPLEMENT**: 单表 `instances(id TEXT PK, name TEXT UNIQUE, subdomain TEXT UNIQUE, region_id INT UNIQUE, region_code TEXT, stun_port INT UNIQUE, container_name TEXT UNIQUE, expires_at TEXT(ISO8601), state TEXT, created_at TEXT)`;迁移 `PRAGMA user_version` 版本数组
- **IMPLEMENT**: `AllocateRegionID` / `AllocateSTUNPort` — 事务内 `SELECT MIN` 找已回收空档 + 冲突重试(最多 3 次);`ReleaseInstance` 删除行释放全部资源
- **IMPORTS**: `modernc.org/sqlite`、`database/sql`
- **GOTCHA**: 唯一索引是防重最后防线,分配必须捕获 UNIQUE 冲突重试;SQLite 开 `_pragma=busy_timeout(5000)` + WAL
- **VALIDATE**: 单测 — 并发分配不重复;删实例后端口可复用

### Task 3: 命名生成器
- **ACTION**: `derp-<日期>-<随机>` 唯一名生成
- **IMPLEMENT**: `func GenerateName(now time.Time) string` → `derp-` + `now.Format("20060102")` + `-` + 4 位 `[a-z0-9]`(crypto/rand);创建流程循环: 生成 → 插入 DB(UNIQUE 冲突则重试,≤10 次)→ 成功即得唯一名
- **MIRROR**: INSTANCE_NAME_FORMAT
- **IMPORTS**: `crypto/rand`、`time`
- **GOTCHA**: 唯一性靠 DB 约束,不靠随机概率;重试上限 10 次后返回明确错误(概率≈0)
- **VALIDATE**: 单测 — 100 次生成无重复;格式正则匹配 `^derp-\d{8}-[a-z0-9]{4}$`

### Task 4: derpMap 生成器
- **ACTION**: 实例 → ACL 格式 JSON
- **IMPLEMENT**: `MapForInstance(i Instance)` 输出 KB 1118 格式(见 DERPMAP_ACL_FORMAT): `omitDefaultRegions:false`、`regions/<regionID>`(字符串 key)、`regionCode` = 日期段(如 `260812`)、`regionName` = 实例名、`nodes[0]` = `{name:"1", regionID, hostName: subdomain, derpPort:443, stunPort}`
- **MIRROR**: DERPMAP_ACL_FORMAT(逐字段一致)
- **IMPORTS**: `encoding/json`
- **GOTCHA**: camelCase 字段名与 KB 1118 逐字一致;`regionID` 在 key 与值同时出现
- **VALIDATE**: 单测反序列化逐字段比对;对照 KB 1118 样例

### Task 5: Docker 编排器
- **ACTION**: 实例容器 create/start/remove 幂等
- **IMPLEMENT**: `type Orchestrator interface { EnsureNode(ctx, Instance) error; RemoveNode(ctx, Instance) error; NodeStatus(ctx, Instance) (State, error) }`;Docker 实现: 容器名 `derp-<id>`、label `app=derp-orch,instance=<id>`、镜像 `deepfal/tailscale-derp:latest`(配置可换 `DERP_IMAGE`)、restart `unless-stopped`、**仅映射 UDP `stunPort:3478/udp`(无 TCP 映射)**、`Binds: [CERTS_DIR:/ssl:ro]`、network `orch-net`、Cmd: `./derper -hostname <subdomain> -a :36666 -certmode manual -certdir /ssl -stun --verify-clients=false --verify-client-url=http://platformd:8080/verify/<id>`
- **IMPLEMENT**: EnsureNode 按 label 查已存在容器,等价则跳过,否则 Remove+Create+Start;NodeStatus 查询容器 Running/Exited/不存在
- **MIRROR**: DERP_CONTAINER_INVOCATION(flag 组合逐字)
- **IMPORTS**: `github.com/docker/docker/client`、`.../api/types/container`、`.../api/types/mount`
- **GOTCHA**: ①不映射宿主 TCP — 443 只走 nginx,防端口竞争;②`--verify-client-url` 含实例 ID 路径;③`--verify-clients=false` 显式传,防误绑宿主 tailscaled socket
- **VALIDATE**: 集成 — `docker ps` 见容器、`docker logs` 无报错;重复 EnsureNode 不重建

### Task 6: nginx SNI 代理
- **ACTION**: stream 模板 + 渲染 + reload(原子)
- **IMPLEMENT**: 模板 `stream { map $ssl_preread_server_name $backend { default derp-unassigned; {{range .Instances}} {{.Subdomain}} derp-{{.ID}}:36666; {{end}} } server { listen 443; ssl_preread on; proxy_pass $backend; } }`;`RenderAndReload`: 写临时文件 → `nginx -t` → 通过则原子 rename + `nginx -s reload`;失败保留旧配置返回错误
- **MIRROR**: N/A — 新约定: 全量生成、校验后原子替换
- **IMPORTS**: `text/template`、`os/exec`
- **GOTCHA**: `default derp-unassigned` 必须存在(未知 SNI 直接断连);`derp-unassigned` 是无效上游名,nginx 对未命中映射的连接直接失败关闭 — 这依赖 map 语义(未匹配 → default → 无效 upstream → 拒绝),测试确认行为
- **VALIDATE**: `nginx -t` 通过;`openssl s_client -servername <实例子域名> -connect <host>:443 -tlsextdebug` 分流到正确容器(docker logs 见握手)

### Task 7: verify 端点(per-instance)
- **ACTION**: 按实例 ID 的准入回调
- **IMPLEMENT**: `POST /verify/{instanceID}`: 查实例 → 不存在或 `state != active` 或 `now >= expires_at` → `{"Allow":false}`;否则 `{"Allow":true}`。200 恒返回 JSON;坏路径 404 + `{"Allow":false}`(协议要求 200 时也尽量给 JSON);日志全量记录 instanceID + NodePublic + Source
- **MIRROR**: VERIFY_REQUEST_RESPONSE(协议逐字)
- **IMPORTS**: `encoding/json`、`net/http`
- **GOTCHA**: ①fail-open 是 derper 默认 — platformd 与容器同 orch-net + restart policy + `/healthz`;②verify 只查 SQLite,不碰 Docker API,毫秒级;③路径参数即实例 ID,须校验格式(防注入,`^[a-z0-9-]{8,}$`)
- **VALIDATE**: `curl -X POST :8080/verify/<id> -d '{"NodePublic":"x","Source":"1.2.3.4"}'` → 活跃 `{"Allow":true}`;过期/不存在 `{"Allow":false}`

### Task 8: 生命周期 sweeper
- **ACTION**: 过期实例自动清理
- **IMPLEMENT**: goroutine,`time.NewTicker(60s)`(间隔可配 `SWEEP_INTERVAL`): `SELECT * FROM instances WHERE expires_at <= now` → 逐个 `RemoveNode` → `ReleaseInstance`(删行释放 regionID/stunPort)→ 全部完成后 `nginx.RenderAndReload` 一次;清理失败重试 3 次,日志记录,不阻塞其他实例;`/healthz` 报告 lastSweepAt
- **MIRROR**: N/A — 新约定: 后台任务错误全部日志化,不 panic
- **IMPORTS**: `time`、标准库
- **GOTCHA**: ①sweeper 与创建流程并发 — 删除行前确认容器已停(先 docker 后 DB,DB 行保留至容器确认消失,失败可重试);②批量 nginx reload 只做一次(性能);③系统时钟:NTP 依赖,文档提示
- **VALIDATE**: 集成 — 创建 TTL=1 分钟实例,等 2 分钟后 `docker ps` 无容器、`instances` 表无行、nginx 配置无该映射

### Task 9: 实例 API
- **ACTION**: 创建/删除/状态
- **IMPLEMENT**: `POST /v1/instances {name?, ttl_hours?}` → 校验 ttl 1–168h → GenerateName → AllocateRegionID/STUNPort → 建行(state=active)→ EnsureNode → RenderAndReload → 返回 `{instance, derpmap}`;`DELETE /v1/instances/{id}` → RemoveNode → ReleaseInstance → RenderAndReload;`GET /v1/instances/{id}` → 实例 + 容器状态 + 剩余秒数;`GET /v1/instances` → 列表。统一 `{ok,data,error}` 包裹,4xx/5xx 映射
- **MIRROR**: N/A — 新约定: API Response Format(common/patterns.md)
- **IMPORTS**: `net/http`(Go 1.22 pattern 语法)
- **GOTCHA**: 创建流程任一失败必须补偿回滚(容器起失败 → 删行释放资源,不留幽灵行);DELETE 幂等(不存在也返回 ok)
- **VALIDATE**: curl 全流程 — 建 → derpmap 可粘贴 → 删 → 容器消失、端口释放

### Task 10: Web 单页
- **ACTION**: 内嵌最基础页面
- **IMPLEMENT**: `//go:embed web/index.html` + `GET /` 返回;单文件 HTML+内联 JS/CSS(无框架): Admin key 输入(localStorage 保存)→ 创建表单(name 可选、ttl 下拉 1h/6h/24h/168h)→ 创建后展示子域名 + derpmap JSON `<textarea readonly>` + 「复制」按钮(Clipboard API)→ 状态区(每 10s 轮询 `GET /v1/instances/{id}`: 容器状态 + 剩余时间倒计时)→ 删除按钮(confirm 弹窗)。状态徽章样式按 design-quality 要求做基础层次(非模板感: 时间轴式实例卡片 + 等宽 JSON 区)
- **MIRROR**: N/A — 新约定: fetch + Bearer 头;XSS 防护: derpmap 永远走 textContent/textarea 渲染,不拼 innerHTML
- **IMPORTS**: embed、net/http
- **GOTCHA**: ①页面不处理登录态过期(401 → 提示重输 key);②不引 CDN(离线可用);③derpmap 展示必须 textContent,防注入
- **VALIDATE**: 浏览器手测 — 创建/复制/倒计时/删除全流程;401 提示正常

### Task 11: 证书与部署
- **ACTION**: 泛域名证书脚本 + docker-compose + README
- **IMPLEMENT**: `deploy/certs/issue-wildcard.sh`: certbot DNS-01 `--domain *.relay.wh.easylan.site` 输出 `/etc/derp-certs`,renew-hook: `docker ps --filter label=app=derp-orch -q | xargs docker restart` + nginx reload;`deploy/docker-compose.yml`: platformd(挂 docker.sock、sqlite 卷、certs:ro、env)+ nginx(stream 配置卷、443:443、restart)+ 显式 `orch-net`;README: 部署步骤、证书签发、租户侧操作(复制 derpmap → 粘贴 ACL → `tailscale up` → `netcheck`)
- **MIRROR**: DERP_CONTAINER_INVOCATION(容器参数)
- **IMPORTS**: N/A(shell + yaml)
- **GOTCHA**: ①renew 后必须重启 derper 容器(证书缓存于进程内);②platformd 容器需 `group_add` 处理 docker.sock 权限;③通配证书覆盖 `*.relay.wh.easylan.site` 单层 — 不可再用更深的子域
- **VALIDATE**: 全新环境 `docker compose up -d` 一次成功;真实 tailnet: 粘贴 ACL → `tailscale up` → `netcheck` 见自定义 region → 实例过期后节点回落官方 DERP

---

## Testing Strategy

### Unit Tests

| Test | Input | Expected Output | Edge Case? |
|---|---|---|---|
| 命名生成 | 100 次 | 无重复,格式 `^derp-\d{8}-[a-z0-9]{4}$` | 是(并发) |
| regionID/stunPort 并发分配 | 2 实例并行 | 无重复;删除后复用 | 是(并发) |
| derpmap 生成 | 实例{name,regionID,subdomain} | 与 KB1118 逐字段一致 | 否 |
| verify 决策 | active/过期/不存在 | Allow true/false/false | 是 |
| verify 非法路径 ID | `../etc` 等 | 400/404,不落库 | 是(注入) |
| sweeper 查询 | 过期+未过期混合 | 仅过期集返回 | 是 |
| ttl 校验 | 0 / 169 / 48 | 400 / 400 / ok | 是(边界) |
| nginx 模板渲染 | 0/1/N 实例 | default + 全部映射 | 是(空列表) |

### Edge Cases Checklist
- [x] 空输入(verify body、ttl、name)
- [x] 最大/最小输入(ttl 1/168;名称截断)
- [x] 非法类型(坏 JSON、非法路径 ID)
- [x] 并发访问(分配、sweeper vs 创建)
- [x] 网络失败(nginx reload 回滚、Docker 不可用、verify 高可用)
- [x] 权限不足(docker.sock 拒绝 → 明确错误)
- [ ] 时钟漂移(过期判定依赖系统时间,NTP 提示)

---

## Validation Commands

### 静态分析
```bash
cd /home/lx/derp-docker-orch && go vet ./... && go build ./...
```
EXPECT: 零错误

### 单元测试
```bash
go test ./... -cover
```
EXPECT: 全部通过,覆盖率 ≥ 80%

### 集成验证(人工,单机)
```bash
docker compose -f deploy/docker-compose.yml up -d
# 浏览器打开 http://<host>:8080 → 输 Admin key → 创建实例(TTL 1h)
curl -s :8080/v1/instances | jq
```
EXPECT: 页面显示 derpmap + 倒计时;`docker ps` 见 `derp-<id>` 容器 up

### 过期回收验证
1. 创建 TTL=1 分钟实例
2. 2 分钟后: `docker ps` 无该容器;`curl /v1/instances` 无该实例;nginx 配置无其映射
3. 实例存在期间 `tailscale netcheck` 可见该 region;过期后消失

### 租户侧端到端(真实 tailnet)
1. 页面复制 derpmap → 粘贴进 ACL policy → 保存
2. `tailscale up` 重启节点
3. `tailscale netcheck` → 自定义 region 出现(如 `260812: 25ms`)
4. `tailscale ping <对端>` → `via DERP(260812)`
5. `docker logs derp-<id>` → verify 调用 + 中继日志可见

### 手动验证清单
- [ ] Web 创建 → derpmap 可复制 → 倒计时走秒
- [ ] 删除按钮 → 容器移除、端口释放
- [ ] 过期自动清理(不用手动删)
- [ ] `openssl s_client -servername <子域名>` 分流正确
- [ ] 过期后新连接被 verify 拒绝
- [ ] 证书续期 → 容器重启后握手正常

---

## Acceptance Criteria
- [ ] 全部任务完成
- [ ] go vet/build/test 通过,覆盖率 ≥ 80%
- [ ] Web 全流程: 创建 → derpmap → 倒计时 → 删除,手测通过
- [ ] 过期自动清理: 容器删除 + 资源释放 + nginx 摘除,验证通过
- [ ] 命名唯一且含日期,格式 `derp-YYYYMMDD-xxxx`
- [ ] 域名派生正确: `derp-xxx.relay.wh.easylan.site`(base 可配置)
- [ ] verify 按实例有效期拒绝过期连接
- [ ] nginx reload 失败回滚,无幽灵配置
- [ ] 创建失败补偿回滚,无幽灵行/幽灵容器

## Completion Checklist
- [ ] derper 参数与 build-container.sh 等价(flag 组合)
- [ ] derpMap 与 KB 1118 格式逐字段一致
- [ ] 错误处理显式传播,不静默吞错
- [ ] 日志: verify 全量审计(instanceID+NodePublic+Source)、sweeper 清理留痕
- [ ] 无硬编码值(域名/端口 base/ttl 全走配置)
- [ ] Web 无 innerHTML 注入(XSS 防护)
- [ ] 无越界范围项(NOT Building 未实现)
- [ ] 自包含 — 实施无需再提问

---

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| regionID 900–999 空间仅 100 并发 | 高 | 中 | 过期即回收复用;并发上限 100 写入 README;`REGION_ID_BASE` 可配 |
| verify fail-open 默认 | 中 | 高 | platformd 同 orch-net + restart policy + /healthz + sweeper 及时摘除过期 |
| 开放中继被滥用 | 中 | 高 | per-instance verify 闸门 + 审计日志 + 手动删除能力 |
| 泛域名证书续期后容器不重载 | 低 | 高 | renew-hook 强制 restart 全部 derper 容器 |
| 过期实例被 ACL 残留引用 | 中 | 中 | derpmap 即删即失效(verify 拒绝);README 提示清理 ACL |
| 时钟漂移致过早/过晚回收 | 低 | 中 | 系统时间 NTP 提示;过期判定统一 now 单一来源 |
| 创建流程部分失败留幽灵 | 中 | 中 | 补偿回滚: 容器失败 → 删行释放资源;sweeper 兜底重扫 |

## Notes

- **verify 协议复核**: 实施时以 `go doc tailscale.com/derp/derpserver` 或上游 derper.go 复核字段名(`NodePublic`/`Source`/`Allow`);headscale issue #1953 为社区确认依据
- **镜像策略**: `DERP_IMAGE` 可配;生产 pin 版本 tag(如 `deepfal/tailscale-derp:v1.98.10`)
- **STUN 端口非标准**: 3478+i 在部分严格 NAT 策略下可能受限;共享 STUN 服务列未来优化
- **多节点预留**: `Orchestrator` 接口为唯一扩展点;hosts 调度不实现
- **regionID 复用告警**: 用户 ACL 若残留过期实例的 derpmap,而 regionID 被新实例复用,旧配置会指向新中继 — verify 按实例 ID 鉴权可缓解(新实例仅允许其自身存活期),README 说明
- **build-container.sh**: 平台上线后仅作 derper 参数参考,README 标注 deprecated
