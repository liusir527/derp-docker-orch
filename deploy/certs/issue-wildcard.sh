#!/bin/sh
# 签发/续期泛域名证书 *.relay.$RELAY_BASE_DOMAIN(Let's Encrypt DNS-01)。
#
# 前置:
#   - certbot + DNS 插件已安装(如 certbot-dns-cloudflare / certbot-dns-aliyun)
#   - DNS 插件凭据已配置
# 用法:
#   RELAY_BASE_DOMAIN=wh.easylan.site CERTS_DIR=/etc/derp-certs \
#     DNS_PLUGIN=dns-cloudflare ./issue-wildcard.sh
#
# 证书输出到 $CERTS_DIR/live 结构,renew-hook 自动重启 derper 容器并重载 nginx。
set -eu

BASE=${RELAY_BASE_DOMAIN:?RELAY_BASE_DOMAIN 必填}
CERTS_DIR=${CERTS_DIR:-/etc/derp-certs}
DNS_PLUGIN=${DNS_PLUGIN:-dns-cloudflare}
DOMAIN="*.relay.${BASE}"

mkdir -p "$CERTS_DIR"

# deploy-hook 先落盘(首次签发与续期共用): 展平证书 + 重启 derper 容器 + 重载 nginx。
cat > "${CERTS_DIR}/deploy-hook.sh" <<'EOF'
#!/bin/sh
# 证书更新后: derper 进程内缓存证书,必须重启容器;nginx 重载加载新证书。
# 由 certbot deploy-hook 调用(仅续期成功时触发)。
CERTS_DIR=${CERTS_DIR:-/etc/derp-certs}
# derper -certmode manual 要求 certdir 下存在 fullchain.crt / privkey.key,由 live 展平。
cp "${CERTS_DIR}/live/derp-relay/fullchain.pem" "${CERTS_DIR}/fullchain.crt"
cp "${CERTS_DIR}/live/derp-relay/privkey.pem"  "${CERTS_DIR}/privkey.key"
docker ps --filter label=app=derp-orch -q | xargs -r docker restart
nginx -s reload 2>/dev/null || true
EOF
chmod +x "${CERTS_DIR}/deploy-hook.sh"

certbot certonly \
  --non-interactive \
  --agree-tos \
  -m "${CERTBOT_EMAIL:-admin@${BASE}}" \
  --"$DNS_PLUGIN" \
  -d "$DOMAIN" \
  --cert-name derp-relay \
  --deploy-hook "${CERTS_DIR}/deploy-hook.sh"

echo "泛域名证书已就绪: ${DOMAIN} → ${CERTS_DIR}"
echo "derper 容器证书目录: ${CERTS_DIR}/live/derp-relay"
