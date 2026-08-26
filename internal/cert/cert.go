// Package cert 集成 Let's Encrypt 通配证书签发与续期。
//
// 流程: 启动时检查 CERTS_DIR/fullchain.pem 是否存在 → 不存在则用 lego + DNS provider
// 签发 *.relay.<base> 与 relay.<base> → 后台 ticker 监控到期(< renewBefore 触发续期)
// → 续期后回调 OnRenew(重启 derper 容器 + reload nginx)。
//
// DNS-01 挑战需配置:
//   CERT_DNS_PROVIDER=dnspod
//   CERT_DNSPOD_API_ID=<id>
//   CERT_DNSPOD_API_TOKEN=<token>
package cert

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/go-acme/lego/v4/certificate"
	"github.com/go-acme/lego/v4/lego"
	"github.com/go-acme/lego/v4/providers/dns/dnspod"
	"github.com/go-acme/lego/v4/registration"
)

// Manager 证书管理。负责首次签发与定期续期。
type Manager struct {
	domain      string   // 证书主域,形如 relay.<base>
	sans        []string // 完整 SAN 列表,通常为 relay.<base> + *.<relay>.<base>
	email       string
	provider    string // lego DNS provider 名,如 "dnspod"
	creds       map[string]string
	certsDir    string
	renewBefore time.Duration
	now         func() time.Time

	// OnRenew 续期后回调(重启 derper 容器 + reload nginx)。
	// 平台启动时注入。失败仅记日志,不影响下一轮续期。
	OnRenew func() error
}

// Options 配置。
type Options struct {
	// Domain 证书主域,如 relay.wh.easylan.site。
	Domain string
	// WildcardBase 泛域名前缀主体,通常为 "relay" 或 ""。如 "relay" + Domain "wh.easylan.site"
	// 产生 SAN: relay.wh.easylan.site + *.relay.wh.easylan.site。
	WildcardBase string
	// Email 证书联系人邮箱(ACME 注册用)。
	Email string
	// Provider lego DNS provider 名,目前仅 "dnspod"。
	Provider string
	// Creds provider 所需凭据(由平台环境变量注入)。
	Creds map[string]string
	// CertsDir 证书输出目录,derper 容器只读挂载 /ssl 指向这里。
	CertsDir string
	// RenewBefore 距到期多久触发续期,默认 30 天。
	RenewBefore time.Duration
}

// New 创建 Manager。
func New(op Options) *Manager {
	if op.RenewBefore == 0 {
		op.RenewBefore = 30 * 24 * time.Hour
	}
	sans := []string{op.Domain}
	if op.WildcardBase != "" {
		sans = append(sans, "*."+op.WildcardBase+"."+op.Domain)
	} else {
		sans = append(sans, "*."+op.Domain)
	}
	return &Manager{
		domain:      op.Domain,
		sans:        sans,
		email:       op.Email,
		provider:    op.Provider,
		creds:       op.Creds,
		certsDir:    op.CertsDir,
		renewBefore: op.RenewBefore,
		now:         time.Now,
	}
}

const (
	fullchainName = "fullchain.pem"
	privkeyName   = "privkey.pem"
)

// Run 阻塞执行,直到 ctx 取消。
// 模式:
//   - active(凭据齐):首次签发(若缺) → 循环监控续期。
//   - passive(无凭据):仅监听证书出现/到期,触发 OnRenew 回调让调用方重启 derper。
//     适用于宝塔面板等外部签发工具:平台不签,只消费证书,负责续期后的热加载。
func (m *Manager) Run(ctx context.Context) error {
	if err := os.MkdirAll(m.certsDir, 0o755); err != nil {
		return fmt.Errorf("创建证书目录: %w", err)
	}

	if m.credsReady() {
		if !m.haveCert() {
			if err := m.obtain(ctx); err != nil {
				return fmt.Errorf("首次签发失败: %w", err)
			}
		} else {
			log.Printf("[cert] 已存在证书,跳过首次签发")
		}
	} else {
		if m.haveCert() {
			log.Printf("[cert] 被动模式:已存在外部证书,仅监听变更不自动续期")
		} else {
			log.Printf("[cert] 被动模式:无凭据且无证书,等待外部(BT 面板)签发 → %s", m.certsDir)
		}
	}

	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if !m.credsReady() {
				m.watchExternal(ctx)
				continue
			}
			if err := m.maybeRenew(ctx); err != nil {
				log.Printf("[cert] 续期检查失败(下轮重试): %v", err)
			}
		}
	}
}

// credsReady 凭据是否齐全(可执行主动签/续)。
func (m *Manager) credsReady() bool {
	if m.provider == "" {
		return false
	}
	for _, v := range m.creds {
		if v == "" {
			return false
		}
	}
	return true
}

// watchExternal 被动模式:检测证书文件出现/变更/临近到期,触发 OnRenew 回调
// 让调用方重启 derper 容器(让 derper 重新读取证书)+ reload nginx。
// 自身不签/不续;续期依赖外部(BT 面板 Let's Encrypt 钩子)。
func (m *Manager) watchExternal(ctx context.Context) {
	have := m.haveCert()
	if !have {
		// 等待证书出现
		return
	}
	exp, err := m.parseExpiryFile()
	if err != nil {
		return
	}
	if m.now().Add(m.renewBefore).After(exp) {
		log.Printf("[cert] 外部证书将在 %s 到期,触发 OnRenew(不自动签)", exp.Format(time.RFC3339))
		if m.OnRenew != nil {
			if err := m.OnRenew(); err != nil {
				log.Printf("[cert] OnRenew 失败(derper 仍可用旧证): %v", err)
			}
		}
	}
}

// haveCert 证书文件是否齐全(derper 容器需要读取)。
func (m *Manager) haveCert() bool {
	for _, p := range []string{fullchainName, privkeyName} {
		if _, err := os.Stat(filepath.Join(m.certsDir, p)); err != nil {
			return false
		}
	}
	return true
}

// obtain 首次签发通配证书。
func (m *Manager) obtain(ctx context.Context) error {
	cli, err := m.newClient(ctx)
	if err != nil {
		return err
	}
	log.Printf("[cert] 首次签发: %v", m.sans)
	req := certificate.ObtainRequest{
		Domains:    m.sans,
		Bundle:     true,
		PrivateKey: m.newKey(),
	}
	cert, err := cli.Certificate.Obtain(req)
	if err != nil {
		return err
	}
	if err := m.write(cert); err != nil {
		return err
	}
	log.Printf("[cert] 签发完成 → %s", m.certsDir)
	return nil
}

// maybeRenew 在到期前 renewBefore 触发续期。
func (m *Manager) maybeRenew(ctx context.Context) error {
	exp, err := m.parseExpiryFile()
	if err != nil {
		// 证书不可读(被外部删):重新签。
		log.Printf("[cert] 证书不可读,重新签发: %v", err)
		return m.obtain(ctx)
	}
	if m.now().Add(m.renewBefore).Before(exp) {
		return nil
	}
	cli, err := m.newClient(ctx)
	if err != nil {
		return err
	}
	log.Printf("[cert] 证书将于 %s 到期,续期", exp.Format(time.RFC3339))
	cert, err := cli.Certificate.Renew(certificate.Resource{
		Domain:      m.domain,
		Certificate: m.mustRead(fullchainName),
		PrivateKey:  m.mustRead(privkeyName),
	}, true, false, "")
	if err != nil {
		return err
	}
	if err := m.write(cert); err != nil {
		return err
	}
	if m.OnRenew != nil {
		if err := m.OnRenew(); err != nil {
			log.Printf("[cert] 续期后回调失败(derper 仍可用旧证): %v", err)
		}
	}
	log.Printf("[cert] 续期完成,新到期 %s", m.parseExpiryDER(cert.Certificate).Format(time.RFC3339))
	return nil
}

// newClient 构造 lego client(含 ACME 注册)。
func (m *Manager) newClient(ctx context.Context) (*lego.Client, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成账户密钥: %w", err)
	}
	user := &acmeUser{email: m.email, key: priv}

	conf := lego.NewConfig(user)
	conf.Certificate.Timeout = 60 * time.Second
	cli, err := lego.NewClient(conf)
	if err != nil {
		return nil, fmt.Errorf("lego client: %w", err)
	}

	// DNS provider 注册。
	switch m.provider {
	case "dnspod":
		// DNSPod v4 API: 单字段 LoginToken = "id,token"。
		loginToken := m.creds["DNSPOD_API_ID"] + "," + m.creds["DNSPOD_API_TOKEN"]
		p, err := dnspod.NewDNSProviderConfig(&dnspod.Config{LoginToken: loginToken})
		if err != nil {
			return nil, fmt.Errorf("dnspod provider: %w", err)
		}
		if err := cli.Challenge.SetDNS01Provider(p); err != nil {
			return nil, fmt.Errorf("设置 DNS-01 provider: %w", err)
		}
	default:
		return nil, fmt.Errorf("不支持的 DNS provider: %s", m.provider)
	}

	// 注册或获取已有账户。
	if user.registration == nil {
		reg, err := cli.Registration.Register(registration.RegisterOptions{TermsOfServiceAgreed: true})
		if err != nil {
			return nil, fmt.Errorf("ACME 注册: %w", err)
		}
		user.registration = reg
	}
	return cli, nil
}

// newKey 生成新 ECDSA 私钥(用于证书请求)。
func (m *Manager) newKey() crypto.PrivateKey {
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	return k
}

// write 落盘 fullchain.pem / privkey.pem。
func (m *Manager) write(cert *certificate.Resource) error {
	if err := os.WriteFile(filepath.Join(m.certsDir, fullchainName), cert.Certificate, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(m.certsDir, privkeyName), cert.PrivateKey, 0o600); err != nil {
		return err
	}
	return nil
}

// parseExpiryFile 读 fullchain.pem 解析第一段证书的 NotAfter。
func (m *Manager) parseExpiryFile() (time.Time, error) {
	b, err := os.ReadFile(filepath.Join(m.certsDir, fullchainName))
	if err != nil {
		return time.Time{}, err
	}
	t, err := parseFirstCertExpiry(b)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// parseExpiryDER 从 PEM bundle 字节解析到期时间。
func (m *Manager) parseExpiryDER(pemBytes []byte) time.Time {
	t, _ := parseFirstCertExpiry(pemBytes)
	return t
}

func (m *Manager) mustRead(name string) []byte {
	b, _ := os.ReadFile(filepath.Join(m.certsDir, name))
	return b
}

// parseFirstCertExpiry 解析 PEM bundle 中第一张证书的 NotAfter。
func parseFirstCertExpiry(pemBytes []byte) (time.Time, error) {
	rest := pemBytes
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			break
		}
		rest = remaining
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return time.Time{}, fmt.Errorf("解析证书: %w", err)
		}
		return c.NotAfter, nil
	}
	return time.Time{}, fmt.Errorf("未找到证书段")
}

// acmeUser lego 注册用户。
type acmeUser struct {
	email        string
	key          *ecdsa.PrivateKey
	registration *registration.Resource
}

func (u *acmeUser) GetEmail() string                       { return u.email }
func (u *acmeUser) GetRegistration() *registration.Resource { return u.registration }
func (u *acmeUser) GetPrivateKey() crypto.PrivateKey        { return u.key }
