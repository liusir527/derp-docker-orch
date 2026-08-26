# Plan: 兑换码 + 管理/兑换双面板(带验证码与资源配额)

**Source**: 自由文本(两次需求整合:原 `/ecc:plan` 双面板需求 + 补充的验证码/资源配额需求)
**Complexity**: Large
**语言**: Go 1.25(平台本体)+ 内嵌单页 HTML/JS(无构建)

## Summary

在现有 DERP 容器 SaaS 平台(platformd)上新增两层:
1. **用户兑换面板**(独立入口 `/redeem`):消耗兑换码 → 创建一个 derp 容器,页面输出 tailscale derpMap ACL JSON;含「配置教程」步骤;不可见管理员入口。
2. **管理面板**(独立入口 `/admin`):admin + 密码 + **图片验证码**登录(防爆破:IP 限速 + 失败计数锁定),登录后批量创建兑换码(天/月/年,可指定自定义前缀),**批量导出兑换码**(文本/CSV),查看容器数量与资源消耗(Docker stats),查看兑换码使用审计(哪个容器用了哪个码、创建时间、过期时间,精确到分钟)。

资源防护:平台评估系统资源(CPU/内存/Docker stats 汇总)做**配额准入**:容器总数上限 + 内存/CPU 使用率水位,超限拒绝创建,防系统崩溃。兑换码本身也带时长,过期由既有 sweeper 清理。

## 现有代码库基线(已读,复用)

| 组件 | 现状 | 本计划动作 |
|---|---|---|
| `internal/store` | instances 单表,SQLite 迁移数组 v1/v2 | 追加 v3:redeem_codes 表 + audit_log 表 |
| `internal/api/router.go` | `{ok,data,error}` 包裹、Bearer 中间件、`GET /` 单页 | 拆分 `/redeem`、`/admin` 双入口 + `/v1/admin/*` 认证组 |
| `internal/api/web/index.html` | 单页管理界面 | 重做:拆成 `web/redeem.html` + `web/admin.html` |
| `internal/docker/orchestrator.go` | EnsureNode/RemoveNode/NodeState | 加 `Stats` 接口方法(资源消耗) |
| `internal/lifecycle/sweeper.go` | 过期清理 | 复用;兑换实例走同生命周期 |
| `internal/config/config.go` | 环境变量 | 加 AdminPassword(必须)、容器配额/资源阈值、验证码 TTL |
| 镜像 `shadowofgost/derper` | nginx-proxy 自动发现 | 复用,不变 |

## Patterns to Mirror

| Category | Source | Pattern |
|---|---|---|
| 兑换码生命周期 | `Headscale-Admin-Pro/app/redeem_service.py` | 码格式 `PREFIX-XXXX-XXXX`、`duration_value/unit(day/month)`、used_at/used_by 标记、BEGIN IMMEDIATE 防并发、预留 token + 15min TTL;`generate_code(prefix)` 已支持自定义前缀(清洗为字母数字,最长 8 位) |
| 验证码 | `Headscale-Admin-Pro/app/blueprints/get_captcha.py` + `auth.py` | PNG 图生成(4 位、字符集 `234689` 去易混);`/get_captcha` 存 session;登录比对;IP 每 60s 限 10 次(`_check_login_rate`) |
| 失败锁定 | `Headscale-Admin-Pro/app/utils.py`(check_account_locked/record_login_failure) | 连续失败 N 次锁 X 分钟,登录重置计数 |
| 时长计算(月) | `Headscale-Admin-Pro/app/redeem_service.py:add_months` | 月末钳制(`min(day, monthrange last_day)`)、年进位 |
| API 包裹 | `internal/api/router.go` | `{ok,data,error}` |
| 实例准入 | `internal/verify/verify.go` | per-instance 按 expires_at 拒绝;审计日志 |
| 时间存储 | `internal/store/instance_store.go` | RFC3339 UTC 字符串;本需求追加**分钟级**展示(±1min 容差) |

## 新增/改动文件

| File | Action | Why |
|---|---|---|
| `internal/store/models.go` | UPDATE | 加 `RedeemCode`、`AuditLog` 结构体 |
| `internal/store/store.go` | UPDATE | 追加迁移 v3:redeem_codes + audit_logs 表 |
| `internal/store/redeem_store.go` | CREATE | 码 CRUD、原子兑换(防并发)、预留机制、使用审计查询 |
| `internal/store/audit_store.go` | CREATE | 操作日志写入/查询(码→容器、创建/过期时间) |
| `internal/redeem/redeem.go` | CREATE | 码生成(`<PREFIX>-XXXX-XXXX`,支持自定义前缀)、时长(day/month/year)校验与过期计算 |
| `internal/captcha/captcha.go` | CREATE | 图片验证码生成 + session 存取 + 校验(参考 HAP) |
| `internal/auth/admin.go` | CREATE | admin 密码登录、会话 cookie、失败锁定、限速中间件 |
| `internal/limits/limits.go` | CREATE | 资源评估:容器数上限 + Docker stats 内存/CPU 水位准入 |
| `internal/docker/orchestrator.go` | UPDATE | `Orchestrator` 接口加 `Stats`;实现容器 stats 汇总 |
| `internal/api/router.go` | UPDATE | `/redeem`、`/admin` 双入口 + `/v1/admin/*`(admin 会话认证)+ `/v1/redeem/*`(码兑换,公开) |
| `internal/api/admin.go` | CREATE | 登录/登出/创建码/列表/统计/资源/审计 handler |
| `internal/api/redeem.go` | CREATE | 兑换 handler:校验码 → 创建实例 → 返回 derpMap ACL |
| `internal/api/web/redeem.html` | CREATE | 用户兑换页(码输入、教程、derpMap 输出、倒计时) |
| `internal/api/web/admin.html` | CREATE | 管理登录(验证码)+ 建码(天/月/年批量)+ 统计/资源/审计 |
| `internal/api/web.go` | UPDATE | embed 双页面路由 |
| `internal/config/config.go` | UPDATE | `ADMIN_PASSWORD`、`MAX_CONTAINERS`、`MEMORY_WATERMARK`、`CPU_WATERMARK`、`CAPTCHA_TTL` 等 |
| `cmd/platformd/main.go` | UPDATE | 组装 captcha/auth/limits 依赖 |
| `README.md` | UPDATE | 双面板使用、环境变量、资源配额说明 |

## Tasks

### Task 1: 存储层 v3 迁移 + 兑换码 store
- **Action**: `redeem_codes` 表 + `audit_logs` 表 + 原子兑换
- **Schema**: `redeem_codes(id INTEGER PK, code TEXT UNIQUE, duration_value INT, duration_unit TEXT CHECK IN('day','month','year'), disabled INT, used_by_instance_id TEXT, used_at TEXT, created_by TEXT, created_at TEXT, reserved_token TEXT, reserved_at TEXT, note TEXT)`
- **Schema**: `audit_logs(id INTEGER PK, kind TEXT, instance_id TEXT, code TEXT, username TEXT, detail TEXT, created_at TEXT)`
- **Mirror**: HAP `redeem_service.py` 的 BEGIN IMMEDIATE + 条件 UPDATE(used_by 空才可占)+ 预留 15min TTL;`calculate_new_expire`/`add_months` 逻辑移植(加 year 单位)
- **GOTCHA**: 兑换与建实例必须**同一事务/原子**:先 reserve 码(token)→ 建实例(容器起成功)→ consume 码(写 instance_id+used_at);失败 release。避免码被占用但容器没建成的半状态
- **Validate**: `go test ./internal/store/...` — 并发兑换同码仅一个成功;过期/禁用/已用码拒绝

### Task 2: 兑换码生成 + 时长计算
- **Action**: `internal/redeem` 包
- **Implement**: `GenerateCode(prefix)` → `<PREFIX>-XXXX-XXXX`(大写字母+数字,`crypto/rand`);`NormalizePrefix` 清洗前缀(仅保留字母数字,去空白,大写,默认 `DERP`,最长 8 位 — HAP 同规则);`ExpiryFrom(durationValue, unit, base)` 支持 day/month/year(年=加 12 个月,复用 add_months 月末钳制);unit 校验与上限(天≤3650、月≤120、年≤10)
- **Mirror**: HAP `generate_code`(prefix 参数)/ `validate_duration` / `add_months`
- **Validate**: 单测 — 生成 100 无重复、格式正则;前缀清洗(含非法字符/过长/空);2026-01-31 +1 月 = 2026-02-28(月末钳制)

### Task 3: 验证码 + admin 认证(防爆破)
- **Action**: `internal/captcha` + `internal/auth` 包
- **Implement**: 纯 Go PNG 验证码(不引大依赖;4 位字符集 `234689` 去易混;随机噪点/干扰线)。`internal/captcha`: `Generate()(id, image)` + 内存 map 存 id→code + TTL(默认 5min,一次有效);校验 `Validate(id, input)`
- **Implement**: `internal/auth`: 登录 `POST /v1/admin/login {username, password, captchaId, captchaText}` → 校验验证码 → 校验密码(`crypto/subtle` 常数时间)→ 发 session cookie(随机 token,存内存/DB session 表,HttpOnly/SameSite=Strict);失败计数:IP 每 60s 限 10 次(参考 HAP `_check_login_rate`)+ 同用户连续 5 次失败锁 15min
- **GOTCHA**: 验证码**一次有效**(登录后/失败后即失效,防重放);会话 token 随机 32 字节;登录页不缓存
- **Validate**: 单测 — 错误验证码拒绝;错 5 次锁定;速率限制生效;登出后会话失效

### Task 4: 资源评估与配额准入
- **Action**: `internal/limits` + Orchestrator.Stats
- **Implement**: `Orchestrator` 接口加 `Stats(ctx) (map[string]*ContainerStat, error)`;Docker 实现: `ContainerList(app=derp-orch)` → `ContainerStats(stream=false)` 汇总 CPU% + 内存(用字节,`blkio`/`cpu_stats` 差值)。`limits.Checker`: `CanCreate(ctx) (ok bool, reason string, report StatsReport)` — 检查:①活跃容器数 < `MAX_CONTAINERS`(默认 100)②总内存使用 < `MEMORY_WATERMARK`(如 80%)③CPU 使用率 < `CPU_WATERMARK`(如 80%);任一超限拒绝并给原因
- **Mirror**: 既有并发上限思路(regionID 900-999 = 100);现改为显式配额
- **GOTCHA**: stats 取**两个采样点差值**算 CPU%;内存水位按 host `/proc/meminfo` 总量基准;超限拒绝在创建 handler 入口先查(不写库、不起容器)
- **Validate**: 集成 — 手动造超限条件看拒绝;单测 — CanCreate 阈值边界

### Task 5: API 路由拆分(双入口)
- **Action**: 路由重组
- **Implement**:
  - `GET /redeem` → redeem.html(公开,无认证)
  - `GET /` 302 → `/redeem`(旧单页入口改为跳转;避免泄露 admin 入口)
  - `GET /admin` → admin.html(公开页面,内嵌登录;页面**不含**任何 admin 链接到 /redeem,反之亦然)
  - 公开: `GET /captcha`, `POST /v1/redeem/redeem`
  - admin 会话: `POST /v1/admin/login`, `POST /v1/admin/logout`, `POST /v1/admin/codes`, `GET /v1/admin/codes`, `GET /v1/admin/stats`, `GET /v1/admin/audit`, `GET /v1/admin/instances`
  - 既有 `GET /v1/instances` 等 Bearer 端点**保留**向后兼容(旧 CLI/脚本),新 admin 端点走 session
- **Mirror**: 现有 `{ok,data,error}` 包裹
- **GOTCHA**: 中间件栈:验证码/login 在 auth 之前;session 校验对 /v1/admin/* 全量应用;兑换端点限速(IP 每 60s 限 5 次)
- **Validate**: `go build ./...` + 路由表测试

### Task 6: 兑换 handler(码 → 容器 → ACL)
- **Action**: `POST /v1/redeem/redeem {code}`
- **Implement**: normalize code(去空格大写)→ limits.CanCreate(超限拒)→ reserve 码(token,15min)→ 复用 `createInstance`(ttl 由码时长决定,不做 ttlHours 参数)→ 建容器成功 → consume 码(写 instance_id/used_at)→ 返回 `{instance, derpMap, expiresAt, remaining}`。任一步失败:release 码 + 补偿删行
- **Mirror**: 既有 `handleCreateInstance` 补偿逻辑(容器失败删行)+ HAP reserve/consume
- **GOTCHA**: 码兑换的实例 ttl 是**码的时长**(天数/月数/年数,转为 time.Duration 上限保护);兑换日志写 audit(kind=redeem, code, instance_id, 创建/过期时间)
- **Validate**: 单测+集成 — 兑换成功返回 derpMap;非法码/已用码/过期码拒绝;配额超限拒绝

### Task 7: 管理 handler(建码/统计/资源/审计)
- **Action**: `internal/api/admin.go`
- **Implement**:
  - `POST /v1/admin/codes {count, durationValue, durationUnit, prefix?, note?}` → 批量生成(1-1000,`prefix` 可选自定义,清洗后 ≤8 位字母数字),返回码列表
  - `GET /v1/admin/codes/export?status=unused` → 批量导出:Content-Disposition 附件下载,`text/plain`(每行一码);支持 `format=csv` 输出 `code,duration_value,duration_unit,created_at`(CSV 转义)
  - `GET /v1/admin/codes?status=all|unused|used` → 分页列表(码、时长、used_by、used_at、created_at)
  - `GET /v1/admin/stats` → {容器总数, 运行中, 过期/待清理, 兑换码总数/未用/已用, 资源报告(内存/CPU)}
  - `GET /v1/admin/instances` → 实例列表 + 各容器资源消耗 + 状态
  - `GET /v1/admin/audit` → 审计日志(时间倒序;含码→容器绑定、创建/过期时间,精确到分钟)
- **Mirror**: 既有 ListInstances/derpMap;API 包裹
- **GOTCHA**: 所有时间展示用 `YYYY-MM-DD HH:MM`(分钟级);导出(可选 CSV)不纳入本里程碑
- **Validate**: 单测 — 建码批量、列表过滤、统计计数正确

### Task 8: 双面板前端
- **Action**: `web/redeem.html` + `web/admin.html`(重写,替换旧 index.html)
- **Implement redeem.html**: 兑换码输入(大输入框)→ 兑换按钮 → 成功区:derpMap JSON(textarea readonly + 复制)+ 子域名 + 到期倒计时;「使用教程」卡片(折叠):①复制 derpMap ②粘贴进 Tailscale ACL ③`tailscale up` ④`netcheck` 验证 — 参考 README 租户步骤。**无**任何 admin 链接/暗示
- **Implement admin.html**: 登录卡片(用户名/密码/验证码图[点击刷新]/错误提示)→ 登录后:①建码表单(数量 + 单位选择 天/月/年 + 数值 + 自定义前缀[可选]+ 备注[可选])②码列表(状态过滤 + **导出按钮**[导出当前过滤集的未用/全部码为 .txt 或 .csv])③统计卡片(容器数/资源/码用量)④审计日志表 ⑤实例列表(含资源)。退出按钮
- **Mirror**: 既有 index.html 的 fetch + textContent 渲染(XSS 防护);design-quality:按现有暗色体系做层次,不做模板感
- **GOTCHA**: 验证码图片 URL 带时间戳防缓存;admin 页面 session cookie 自动携带;401 → 弹登录
- **Validate**: 浏览器手测全流程

### Task 9: 配置 + 组装 + 文档
- **Action**: config 新变量、main.go 组装、README
- **Implement** config 新增: `ADMIN_PASSWORD`(必填,缺失启动失败)、`ADMIN_USERNAME`(默认 admin)、`MAX_CONTAINERS`(默认 100)、`MEMORY_WATERMARK_PERCENT`(默认 80)、`CPU_WATERMARK_PERCENT`(默认 80)、`CAPTCHA_TTL_SECONDS`(默认 300)、`REDEEM_IP_RATE`(默认 5/60s)、`ADMIN_LOGIN_IP_RATE`(默认 10/60s)、`LOGIN_MAX_FAILS`(默认 5)、`LOGIN_LOCK_MINUTES`(默认 15)
- **Implement**: main.go 组装 captcha/auth/limits 注入 api.New;README 更新双面板入口、环境变量表、资源配额说明、安全提示(密码强度、会话、验证码防爆破)
- **GOTCHA**: `ADMIN_PASSWORD` 与旧 `ADMIN_API_KEY` 并存(旧 Bearer 保留兼容,新 session 认证;README 说明两者用途)
- **Validate**: `go vet ./... && go build ./...`

## Validation

```bash
cd /home/lx/derp-docker-orch
go vet ./... && go build ./...
go test ./... -race -cover
# 集成(人工):
docker compose -f deploy/docker-compose.yml up -d
# /redeem 兑换流程; /admin 登录(验证码)→ 建码 → 兑换; /admin 看统计/资源/审计
```

## Risks

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| 兑换并发抢同码 | 中 | 中 | BEGIN IMMEDIATE + 条件 UPDATE + 预留 token 15min TTL(HAP 已验证模式) |
| 验证码被 OCR/爆破 | 中 | 低 | 4 位去易混字符 + 一次有效 + IP 限速 10/60s + 失败锁定 15min |
| Docker stats CPU% 计算错误 | 中 | 中 | 双采样点差值;先用 docker stats CLI 输出对拍测试 |
| 配额误杀(内存水位误报) | 中 | 中 | 水位可配;超限仅拒绝**新创建**,不动既有容器 |
| session 伪造 | 低 | 高 | 32 字节随机 token + HttpOnly + SameSite=Strict;服务端存 token |
| 兑换与建容器原子性 | 中 | 中 | reserve→起容器→consume;失败 release+补偿删行,不留半状态 |
| 旧 Bearer 与新 session 并存混乱 | 低 | 中 | 文档明确:对外兑换/admin 走 session;`/v1/instances` Bearer 保留内部/脚本用 |

## Acceptance

- [ ] `/redeem` 兑换码 → 创建容器 → 输出 derpMap ACL JSON → 倒计时;非法/已用/过期码拒绝
- [ ] `/redeem` 页含配置教程;页面不含管理员入口(反之亦然)
- [ ] `/admin` admin+密码+验证码登录;错 5 次锁 15min;IP 限速
- [ ] `/admin` 按天/月/年批量建码(1-1000,支持自定义前缀);码列表/过滤
- [ ] `/admin` 批量导出兑换码(.txt 每行一码 / .csv 带时长字段,仅未用码)
- [ ] `/admin` 查看容器数量 + 资源消耗(Docker stats 汇总)
- [ ] `/admin` 审计日志:码被哪个容器使用、容器创建时间、过期时间(分钟级)
- [ ] 资源配额:容器数/内存/CPU 超限拒绝创建,防系统崩溃
- [ ] 过期实例仍由 sweeper 自动清理(兑换实例同样)
- [ ] `go vet/build/test -race` 通过,覆盖率 ≥ 80%
- [ ] 双入口隔离:兑换面板无 admin 路径泄露

## NOT Building

- 用户账户体系/注册(兑换即用,不建账号;HAP 的 users 表不移植)
- 扫码/邮件分发;导出可选扩展(批量 CSV 已含在本里程碑)
- 多宿主节点池调度
- 计费/订单
