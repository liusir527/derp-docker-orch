# DERP 容器 SaaS 平台(nginx-proxy 模式)

多租户临时 DERP 中继平台:Web 一键创建 DERP 实例,复制 derpMap 粘贴进 Tailscale ACL 即可使用;实例有有效期,到期自动删除容器并回收资源。所有 derper 容器通过 nginx-proxy 自动发现和路由。


## 架构

```
客户端 HTTPS:443 → BT-nginx (server_name *.relay.<base>)
                    ↓ proxy_pass http://127.0.0.1:1443
                 jwilder/nginx-proxy (自动发现 VIRTUAL_HOST)
                    ↓ 容器 IP:80
                 derp-<实例> 容器 (:80 HTTP, --certmode=none)

客户端 UDP:3478+i → derp-<实例> 容器 (宿主 STUN 端口)

platformd (Go): Web 界面 + 实例 API + sweeper 过期清理 + Docker 编排
```


## 部署

前置: 一台有公网 IP 的宿主机、Docker、域名 DNS(通配 A 记录)。

数据目录: SQLite 数据库持久化在宿主机 `/data/derp-orch/`(容器内 `/data` 的 bind mount),删除重建容器数据不丢。

### 1. 启动 nginx-proxy

```bash
docker run -d \
  --name nginx-proxy \
  --restart unless-stopped \
  -p 127.0.0.1:1443:80 \
  -v /var/run/docker.sock:/tmp/docker.sock:ro \
  jwilder/nginx-proxy:1.11.6
```

### 2. 配置 BT-nginx 站点

在宝塔面板 → 网站 → 添加站点:

- **域名**: `*.relay.wh.easylan.site` 和 `default.relay.wh.easylan.site`
- **SSL**: 签发泛域名证书 `*.relay.wh.easylan.site`
- **反向代理**: `http://127.0.0.1:1443`

### 3. 构建本地镜像并导出(可选,离线/预构建)

本机开发环境构建镜像,导出 tar 包,服务器导入后即可复用,无需联网拉 Go 依赖。

```bash
# 本机: 构建镜像
docker build -t derp-orch:latest .

# 本机: 导出镜像为 tar 包
docker save -o derp-orch.tar derp-orch:latest
```

```bash
# 服务器: 导入镜像
docker load -i derp-orch.tar

# 服务器: 确认
docker images | grep derp-orch
```

### 4. 启动平台

```bash
export RELAY_BASE_DOMAIN=wh.easylan.site
export ADMIN_API_KEY="$(openssl rand -hex 32)"
export ADMIN_PASSWORD="$(openssl rand -base64 18)"
docker compose -f deploy/docker-compose.yml up -d
```

打开 `http://<host>:8080/redeem` 使用兑换面板;`http://<host>:8080/admin` 进管理面板(admin + 上述 `ADMIN_PASSWORD` + 验证码)。

> 若服务器已导入预构建镜像,可去掉 compose 中 `platformd` 的 `build` 块,只保留 `image: derp-orch:latest`,
> 或直接用 `docker run` 方式启动(需挂 docker.sock)。

### 5. DNS

```
*.relay.wh.easylan.site  A  <宿主公网 IP>
```

## 使用(租户侧 — 兑换面板)

访问 `http://<host>:8080/redeem`:

1. 输入兑换码(由管理员提供),点击兑换,平台创建专属 DERP 容器
2. 复制页面 derpMap JSON
3. 打开 [Tailscale 管理台 → ACL](https://login.tailscale.com/admin/acls),把 derpMap JSON 粘贴进 policy 文件顶层,保存
4. 节点重启 tailscale:`tailscale up`(或重启服务)
5. 验证:`tailscale netcheck` 应看到自定义 region(如 `260812: 25ms`);`tailscale ping <对端>` 输出 `via DERP(260812)` 表示中继生效
6. 到期后平台自动回收,无需手动操作

## 管理面板

访问 `http://<host>:8080/admin`,admin + 密码 + **图片验证码**登录(防爆破:IP 限速 + 连续失败锁定)。

- **批量创建兑换码**:数量、时长(天/月/年)、自定义前缀(≤8 位字母数字,默认 `DERP`)
- **批量导出**:未使用兑换码导出 `.txt`(每行一码)或 `.csv`(含时长字段)
- **平台概览**:容器数量、宿主内存使用率、容器 CPU
- **审计日志**:兑换码被哪个实例使用、实例创建/过期时间(分钟级)

## 多地区(第二台服务器)

一个兑换码池跨地区共享:管理员只在一个控制面建码,用户兑换时**自选地区**,平台在该地区创建中继。
控制面仍只跑在主地区(A),通过 HTTPS + Token 调地区代理 `regiond`,**不需要把 B 的 Docker API 暴露公网**。

```
用户兑换(选地区)
      │
      ▼
A: platformd ──HTTPS+Token──► B: regiond ──docker.sock──► derper 容器
      │                            │
      └── 本地区直接 docker.sock ───┴── nginx-proxy + BT-nginx(各自通配域名)
```

### 部署 B(以 `cd` 地区为例)

1. **DNS / 证书**:`*.relay.cd.easylan.site` 与 `relay.cd.easylan.site` → B 公网 IP;签发通配证书到 `/www/server/panel/vhost/cert/relay.cd.easylan.site/`(agent 用 `agent.relay.cd.easylan.site`,同属该通配证书)。
2. **B 的宝塔 nginx** 两个 `server` 块:
   - `relay.cd.easylan.site *.relay.cd.easylan.site` → `proxy_pass 127.0.0.1:1443`(WebSocket 用 `$connection_upgrade`,超时 3600s)
   - `agent.<base>` → `proxy_pass 127.0.0.1:8081`(供控制面调用,建议只放行 A 的 IP)
   - 完整示例见 `deploy/nginx-region.conf.example`
3. **启动 B**:
   ```bash
   export REGION_AGENT_TOKEN="$(openssl rand -hex 32)"
   export DERP_ORCH_IMAGE=ghcr.io/<owner>/<repo>:latest   # 与 A 同一镜像
   docker compose -f deploy/region-compose.yml up -d
   ```
   B 内存较小时建议单独构建精简镜像(约 20MB):交叉编译 `regiond` 后 `docker build -f deploy/Dockerfile.regiond -t derp-orch-regiond:latest .`,compose 里设 `DERP_ORCH_IMAGE=derp-orch-regiond:latest`。
4. **A 后台登记**:管理面板 →「地区管理」→ 新增
   - ID `cd`、名称 `西南`、基础域名 `cd.easylan.site`、regionCode `cd`
   - 代理地址 `https://agent.relay.cd.easylan.site`、令牌填第 3 步的 `REGION_AGENT_TOKEN`
   - 点「测试」应返回 Docker 正常 + 内存/CPU 概况。
5. 兑换页会出现地区下拉框;`wh`(本地)与 `cd` 共用同一批兑换码。

### 说明

- `regionID`(900-999)与 STUN 端口(3478+)仍是**全局唯一池**,跨地区统一分配,derpMap 不会冲突;用 `REGION_ID_MAX` 控制上限。
- 若某地区宿主已有服务占用 `3478/udp`(例如 headscale 的内置 DERP),把控制面的 `STUN_PORT_BASE` 提高到未占用段(如 `3479`),避免新实例端口映射失败。
- 每个地区必须有**独立基础域名**(通配 DNS 只能指向一台机器),实例子域名按地区域名生成。
- 地区下已有实例时禁止修改基础域名(存量中继会失联);需先清空实例。
- 证书续期:控制面的 `cert` 管理只负责本地地区;B 的证书由 B 的宝塔续期,续期后重启 B 上的 derper 容器(`docker restart $(docker ps -q --filter label=app=derp-orch)`)。
- 资源配额按地区统计(容器数可用地区单独覆盖;远程宿主内存/CPU 由 `regiond` 上报)。

## API

- `/v1/admin/*`:管理端点,需 admin 会话 cookie(登录后自动携带)
- `/v1/redeem/redeem`:兑换(公开,IP 限速),请求体 `{code, region?}`;`region` 省略为本地地区
- `/v1/regions`:公开地区列表(兑换页下拉框)
- `/v1/instances`:旧 Bearer 端点(`Authorization: Bearer <ADMIN_API_KEY>`),保留向后兼容

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/redeem` | 用户兑换面板 |
| GET | `/admin` | 管理面板(登录页) |
| GET | `/captcha` | 验证码 PNG(响应头 `X-Captcha-Id`) |
| GET | `/healthz` | 健康检查 |
| GET | `/v1/regions` | 启用中的地区列表 `{regions:[{id,name,baseDomain,regionCode}]}` |
| GET/POST | `/v1/admin/regions` | 地区列表 / 新增地区 |
| PUT/DELETE | `/v1/admin/regions/{id}` | 修改 / 删除地区(有实例不可删、不可改域名) |
| POST | `/v1/admin/regions/{id}/test` | 探测地区代理连通性与宿主资源 |
| POST | `/v1/admin/login` | 登录(验证码+密码) |
| POST | `/v1/admin/logout` | 登出 |
| POST | `/v1/admin/codes` | 批量建码 `{count, durationValue, durationUnit, prefix?, note?}` |
| GET | `/v1/admin/codes` | 码列表 `?status=all\|unused\|used` |
| GET | `/v1/admin/codes/export` | 导出 `?status=&format=txt\|csv` |
| GET | `/v1/admin/stats` | 容器数 + 资源 + 码用量 |
| GET | `/v1/admin/instances` | 实例列表 + 各容器资源 |
| GET | `/v1/admin/audit` | 审计日志(分钟级) |
| POST | `/v1/redeem/redeem` | 兑换 `{code, region?}` → 实例 + derpMap |
| POST | `/v1/instances` | 旧:创建实例(Bearer) |
| POST | `/verify/{instanceID}` | derper 准入回调(协议内部) |

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `RELAY_BASE_DOMAIN` | — 必填 | 域名派生基底,如 `wh.easylan.site` |
| `ADMIN_API_KEY` | — 必填 | 旧 Bearer 管理密钥(脚本/内部) |
| `ADMIN_PASSWORD` | — 必填 | 管理面板登录密码 |
| `ADMIN_USERNAME` | admin | 管理面板用户名 |
| `SESSION_TTL_HOURS` | 12 | 管理会话有效期 |
| `MAX_CONTAINERS` | 100 | 活跃容器上限(资源配额) |
| `MEMORY_WATERMARK_PERCENT` | 80 | 宿主内存使用率水位%,超限拒建容器 |
| `CPU_WATERMARK_PERCENT` | 80 | 容器总 CPU% 水位,超限拒建容器 |
| `CAPTCHA_TTL_SECONDS` | 300 | 验证码有效期 |
| `REDEEM_IP_RATE_LIMIT` | 5 | 兑换接口每窗口每 IP 限次 |
| `REDEEM_IP_WINDOW_SECONDS` | 60 | 兑换限速窗口 |
| `LOGIN_IP_RATE_LIMIT` | 10 | 登录接口每窗口每 IP 限次 |
| `LOGIN_IP_WINDOW_SECONDS` | 60 | 登录限速窗口 |
| `LOGIN_MAX_FAILS` | 5 | 连续失败锁定阈值 |
| `LOGIN_LOCK_MINUTES` | 15 | 锁定时长 |
| `DEFAULT_TTL_HOURS` | 24 | 默认有效期(旧端点) |
| `REGION_ID_BASE` | 900 | regionID 起点 |
| `REGION_ID_MAX` | 999 | regionID 上限(编号空间耗尽即拒建) |
| `REGION_SLUG` | wh | 本地地区 ID(客户端选地区用) |
| `REGION_NAME` | 华中 | 本地地区显示名 |
| `REGION_CODE` | wh | 本地地区 derpMap RegionCode |
| `REGION_AGENT_TOKEN` | — 必填(regiond) | 地区代理 Bearer Token,与控制面登记的一致 |
| `LISTEN_ADDR` | :8081(regiond) | 地区代理监听地址 |
| `AGENT_TLS_CERT` / `AGENT_TLS_KEY` | 空 | 可选,regiond 直接终止 TLS(否则由前置 nginx 终止) |
| `STUN_PORT_BASE` | 3478 | 宿主 STUN UDP 端口起点 |
| `DERP_IMAGE` | shadowofgost/derper:main-2026-08-15 | derper 镜像 |
| `SWEEP_INTERVAL_SECONDS` | 60 | 过期扫描间隔 |
| `DB_PATH` | /data/derp-orch.db | SQLite 路径 |

## 限制与说明

- **资源配额**:容器数上限 + 内存/CPU 水位双保险,超限拒绝新创建,防宿主崩溃
- **regionID 空间 900-999 仅 100 个并发实例**(Tailscale 官方保留段);到期即回收复用,多地区共享同一池
- 同一 tailnet 若同时引用平台多个实例的 derpMap,regionID 不会冲突(平台全局唯一分配)
- ACL 里残留已删除实例的 derpMap 无害:derper 会拒绝过期实例的连接
- 旧 `build-container.sh` 已废弃,仅作 derper 参数参考
- 新镜像 `shadowofgost/derper` 使用 `--certmode=none`,证书由 BT-nginx 终止;derpMap 格式参考 [Tailscale KB 1118](https://tailscale.com/kb/1118/custom-derp-servers)
