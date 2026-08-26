// Package config 加载平台环境变量配置。
// 所有平台行为参数均来自环境变量,不硬编码(见 common/coding-style.md 无魔法数约定)。
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config 平台全部配置项。
type Config struct {
	// RelayBaseDomain 派生子域名的基底域名,如 wh.easylan.site。
	// 实例子域名 = derp-<name>.relay.<RelayBaseDomain>。
	RelayBaseDomain string
	// DefaultTTL 创建实例时的默认有效期,缺省 24h。
	DefaultTTL time.Duration
	// RegionIDBase regionID 分配起点,官方保留 900-999 给自定义 DERP。
	RegionIDBase int
	// STUNPortBase 宿主 STUN UDP 端口分配起点,标准端口 3478。
	STUNPortBase int
	// DockerHost Docker Engine API 地址。
	DockerHost string
	// AdminAPIKey 平台管理 API 密钥(Bearer 认证),必填。
	AdminAPIKey string
	// CERTSDir 泛域名证书目录,只读挂载进 derper 容器 -certdir /ssl。
	// DEPRECATED: certmode=none 时不挂载,保留字段保持向后兼容。
	CertsDir string
	// DERPImage derper 容器镜像。
	DERPImage string
	// DockerNetwork 容器编排网络。
	// DEPRECATED: 改用默认 bridge 网(与 nginx-proxy 同),保留字段保持向后兼容。
	DockerNetwork string
	// VerifyBaseURL platformd 自身在 orch-net 中的地址,用于拼 verify URL。
	// DEPRECATED: 新镜像 --verify-clients=false,保留字段保持向后兼容。
	VerifyBaseURL string
	// SweepInterval 过期实例扫描间隔。
	SweepInterval time.Duration
	// ListenAddr platformd HTTP 监听地址。
	ListenAddr string
	// DERPPort derper 中继端口(derpMap 输出)。
	// 默认 443(BT-nginx 终止 TLS,客户端连 443)。
	DERPPort int
	// CertDNSProvider 证书 DNS-01 provider,默认 dnspod。
	CertDNSProvider string
	// CertDNSPodID DNSPod API ID。
	CertDNSPodID string
	// CertDNSPodToken DNSPod API Token。
	CertDNSPodToken string
	// CertEmail ACME 注册邮箱。
	CertEmail string
	// CertRenewBeforeDays 距到期多少天续期,默认 30。
	CertRenewBeforeDays int
}

// Load 从环境变量读取配置,缺失必填项返回错误。
func Load() (*Config, error) {
	c := &Config{
		RelayBaseDomain: os.Getenv("RELAY_BASE_DOMAIN"),
		DefaultTTL:      durEnv("DEFAULT_TTL_HOURS", 24, time.Hour),
		RegionIDBase:    intEnv("REGION_ID_BASE", 900),
		STUNPortBase:    intEnv("STUN_PORT_BASE", 3478),
		DockerHost:      strEnv("DOCKER_HOST", "unix:///var/run/docker.sock"),
		AdminAPIKey:     os.Getenv("ADMIN_API_KEY"),
		CertsDir:        strEnv("CERTS_DIR", "/etc/derp-certs"),
		DERPImage:       strEnv("DERP_IMAGE", "shadowofgost/derper:main-2026-08-15"),
		DockerNetwork:   strEnv("DOCKER_NETWORK", "bridge"),
		VerifyBaseURL:   strEnv("VERIFY_BASE_URL", "http://platformd:8080"),
		SweepInterval:   durEnv("SWEEP_INTERVAL_SECONDS", 60, time.Second),
		ListenAddr:      strEnv("LISTEN_ADDR", ":8080"),
		DERPPort:        intEnv("DERP_PORT", 443),
		CertDNSProvider: strEnv("CERT_DNS_PROVIDER", "dnspod"),
		CertDNSPodID:    os.Getenv("CERT_DNSPOD_API_ID"),
		CertDNSPodToken: os.Getenv("CERT_DNSPOD_API_TOKEN"),
		CertEmail:       strEnv("CERT_EMAIL", ""),
		CertRenewBeforeDays: intEnv("CERT_RENEW_BEFORE_DAYS", 30),
	}

	var missing []string
	if c.RelayBaseDomain == "" {
		missing = append(missing, "RELAY_BASE_DOMAIN")
	}
	if c.AdminAPIKey == "" {
		missing = append(missing, "ADMIN_API_KEY")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("缺失必填环境变量: %v", missing)
	}
	return c, nil
}

func strEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func intEnv(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}

// durEnv 读取整数环境变量,乘以 unit 得到 duration。
func durEnv(key string, def int, unit time.Duration) time.Duration {
	return time.Duration(intEnv(key, def)) * unit
}

// Subdomain 派生实例子域名:derp-<name>.relay.<base>。
// name 已含 derp- 前缀(由 derp.GenerateName 生成)时,只去重不加。
func (c *Config) Subdomain(name string) string {
	short := strings.TrimPrefix(name, "derp-")
	return "derp-" + short + ".relay." + c.RelayBaseDomain
}
