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
docker compose -f deploy/docker-compose.yml up -d
```

打开 `http://<host>:8080`,输入管理密钥。

> 若服务器已导入预构建镜像,可去掉 compose 中 `platformd` 的 `build` 块,只保留 `image: derp-orch:latest`,
> 或直接用 `docker run` 方式启动(需挂 docker.sock)。

### 5. DNS

```
*.relay.wh.easylan.site  A  <宿主公网 IP>
```

## 使用(租户侧)

1. Web 页面创建实例(选有效期),复制 derpMap JSON
2. 打开 [Tailscale 管理台 → ACL](https://login.tailscale.com/admin/acls),把 derpMap JSON 粘贴进 policy 文件顶层,保存
3. 节点重启 tailscale:`tailscale up`(或重启服务)
4. 验证:`tailscale netcheck` 应看到自定义 region(如 `260812: 25ms`);`tailscale ping <对端>` 输出 `via DERP(260812)` 表示中继生效
5. 到期后平台自动回收,无需手动操作

## API

所有 `/v1/*` 端点需 `Authorization: Bearer <ADMIN_API_KEY>`。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/` | Web 管理界面 |
| GET | `/healthz` | 健康检查(含 lastSweepAt) |
| POST | `/v1/instances` | 创建实例 `{ttlHours: 1-168}`(缺省 24) |
| GET | `/v1/instances` | 实例列表 |
| GET | `/v1/instances/{id}` | 详情 + 容器状态 + 剩余秒数 |
| DELETE | `/v1/instances/{id}` | 立即删除(幂等) |
| POST | `/verify/{instanceID}` | derper 准入回调(协议内部) |

## 环境变量

| 变量 | 默认 | 说明 |
|---|---|---|
| `RELAY_BASE_DOMAIN` | — 必填 | 域名派生基底,如 `wh.easylan.site` |
| `ADMIN_API_KEY` | — 必填 | 管理密钥 |
| `DEFAULT_TTL_HOURS` | 24 | 默认有效期 |
| `REGION_ID_BASE` | 900 | regionID 起点(官方自定义范围 900-999,并发上限 100) |
| `STUN_PORT_BASE` | 3478 | 宿主 STUN UDP 端口起点 |
| `CERTS_DIR` | /etc/derp-certs | 泛域名证书目录 |
| `DERP_IMAGE` | shadowofgost/derper:main-2026-08-15 | derper 镜像 |
| `DOCKER_NETWORK` | bridge | DEPRECATED |
| `VERIFY_BASE_URL` | http://platformd:8080 | verify URL 前缀 |
| `SWEEP_INTERVAL_SECONDS` | 60 | 过期扫描间隔 |
| `DB_PATH` | /data/derp-orch.db | SQLite 路径 |

## 限制与说明

- **regionID 空间 900-999 仅 100 个并发实例**(Tailscale 官方保留段);到期即回收复用
- 同一 tailnet 若同时引用平台多个实例的 derpMap,regionID 不会冲突(平台全局唯一分配)
- ACL 里残留已删除实例的 derpMap 无害:derper 会拒绝过期实例的连接
- 旧 `build-container.sh` 已废弃,仅作 derper 参数参考
- 新镜像 `shadowofgost/derper` 使用 `--certmode=none`,证书由 BT-nginx 终止;derpMap 格式参考 [Tailscale KB 1118](https://tailscale.com/kb/1118/custom-derp-servers)
