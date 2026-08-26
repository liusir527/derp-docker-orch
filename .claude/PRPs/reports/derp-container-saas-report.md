# Implementation Report: DERP 容器 SaaS 平台

## Summary

多租户临时 DERP 中继平台已实现:Go 平台(platformd)+ 内嵌 Web 单页 + Docker 编排 + nginx SNI 分流 + per-instance verify 准入 + sweeper 过期清理。全部 11 个任务完成,单测 23 个全绿,真实环境集成验证通过(含补偿回滚路径)。

## Assessment vs Reality

| Metric | Predicted (Plan) | Actual |
|---|---|---|
| Complexity | Large | Large(单测修复 3 处设计缺陷,见下) |
| Confidence | 7/10 | 8/10 — 除「真实 tailnet 端到端」外全部验证 |
| Files Changed | 19 | 25(13 源码 + 6 测试 + 部署/文档 6) |

## Tasks Completed

| # | Task | Status | Notes |
|---|---|---|---|
| 1 | 工程骨架与配置 | done | 缺失必填 env 启动失败 |
| 2 | 存储层 SQLite | done | 分配器实现中重构(见 Deviations) |
| 3 | 命名生成器 | done | crypto/rand + DB 唯一兜底 |
| 4 | derpMap 生成器 | done | KB1118 逐字段单测比对 |
| 5 | Docker 编排器 | done | v28 API 签名适配 |
| 6 | nginx SNI 代理 | done | 模板内嵌代码,default 用 127.0.0.1:1 |
| 7 | verify 端点 | done | per-instance,协议单测 |
| 8 | 生命周期 sweeper | done | 删容器→删行→nginx 批量 |
| 9 | 实例 API | done | 补偿回滚真实环境验证 |
| 10 | Web 单页 | done | embed,textContent 防注入 |
| 11 | 证书 + 部署 + README | done | certbot deploy-hook 展平 fullchain.crt/privkey.key |

## Validation Results

| Level | Status | Notes |
|---|---|---|
| Static Analysis | Pass | go build + go vet 零错误 |
| Unit Tests | Pass | 23 tests,6 包全绿 |
| Build | Pass | `go build ./...` 零错误 |
| Integration | Pass(API 级) | healthz/401/创建回滚/verify 协议/Web 页/幂等删除 全过 |
| Edge Cases | Pass | ttl 边界、非法 ID、并发分配、空列表渲染、坏 JSON |

> 完整容器级集成(真实 derper 容器)未执行:本机拉取 `deepfal/tailscale-derp` 镜像超时(Docker Hub 网络)。镜像缺失路径已在真实环境验证(创建失败 → 补偿回滚 → 列表为空)。

## Files Changed

| File | Action |
|---|---|
| `cmd/platformd/main.go` | CREATED |
| `internal/config/config.go` | CREATED |
| `internal/store/{store,models,instance_store}.go` | CREATED |
| `internal/derp/{namegen,derpmap}.go` | CREATED |
| `internal/docker/orchestrator.go` | CREATED |
| `internal/nginx/proxy.go` | CREATED |
| `internal/verify/verify.go` | CREATED |
| `internal/lifecycle/sweeper.go` | CREATED |
| `internal/api/{router,instances,web}.go` + `web/index.html` | CREATED |
| `deploy/{docker-compose.yml,nginx/nginx.conf,certs/issue-wildcard.sh}` | CREATED |
| `README.md` | CREATED |
| `go.mod` / `go.sum` | CREATED |
| 6 个 `*_test.go` | CREATED |
| `build-container.sh` | UPDATED(deprecated 标注) |

## Deviations from Plan

| What | Why |
|---|---|
| nginx map default 用 `127.0.0.1:1` 而非 `derp-unassigned` | `derp-unassigned` 非合法 upstream,会导致 nginx -t 失败;127.0.0.1:1 连接即拒,语义等价 |
| stream 模板内嵌 Go 代码,未另建 `stream.conf.tmpl` 文件 | 单一来源,免双份维护;渲染函数可单测 |
| `resolver 127.0.0.11` 加入 stream 配置 | nginx 变量 proxy_pass 需运行时解析容器 DNS 主机名 |
| 资源分配改为 `CreateInstanceAllocated`(单事务扫描+插入) | 计划中「占位插入+回滚」方案并发下失效(回滚即释放,全部拿到 900);事务内真实插入 + UNIQUE 重试才是正确互斥 |
| nginx 命令 nil 守卫 | 测试与无 nginx 环境可用 |
| compose 网络 `name: orch-net` 非 external | 由 compose 自动创建,Docker API 按固定名挂载 |

## Issues Encountered

| Issue | Resolution |
|---|---|
| docker v28 API 变更(ContainerCreate 2 值、PortBinding 移入 nat 包) | 读本地模块源码修正 |
| proxy.golang.org TLS 超时 | GOPROXY=https://goproxy.cn,direct |
| 并发分配全部返回 900(占位回滚方案失效) | 重构为事务内扫描+真实插入(见 Deviations) |
| 8080/18080 被占用 | 集成测试用 28080 |
| derper 镜像拉取超时(Docker Hub) | 跳过容器级集成,API 级验证 + 镜像缺失路径真实验证 |
| 测试经 mux 分发才可取 PathValue | 单测改经 http.NewServeMux 路由(verify/api 两处) |

## Tests Written

| Test File | Tests | Coverage |
|---|---|---|
| `internal/derp/namegen_test.go` | 2 | 格式/唯一性 |
| `internal/derp/derpmap_test.go` | 1 | KB1118 逐字段 |
| `internal/store/instance_store_test.go` | 5 | 并发分配/复用/空档/过期/未找到 |
| `internal/verify/verify_test.go` | 5 | 活跃/过期/不存在/非法 ID/坏 body |
| `internal/nginx/proxy_test.go` | 3 | 空/单/多实例渲染 |
| `internal/lifecycle/sweeper_test.go` | 2 | 过期清理/无过期 no-op |
| `internal/api/instances_test.go` | 5 | ttl 校验/补偿回滚/成功响应/幂等删除/regionCode |

## Next Steps

- [x] 集成验证(API 级)完成
- [ ] 完整容器级集成: 拉取镜像后 `docker compose up`,真实 tailnet 端到端(粘贴 ACL → netcheck)
- [ ] Code review via `/code-review`
- [ ] `deploy/docker-compose.yml` 中 platformd 换预构建镜像(生产)
- [ ] 环境变量文档核对(docker-compose 与 README 一致)
