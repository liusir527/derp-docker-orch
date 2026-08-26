// platformd 是 DERP 容器 SaaS 平台主进程。
// 职责: 加载配置 → 迁移数据库 → 构建编排器/nginx 代理/sweeper → 启动 HTTP 服务。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"derp-orch/internal/api"
	"derp-orch/internal/cert"
	"derp-orch/internal/config"
	dockerpkg "derp-orch/internal/docker"
	"derp-orch/internal/lifecycle"
	"derp-orch/internal/store"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("配置错误: %v", err)
	}

	st, err := store.Open(strEnv("DB_PATH", "/data/derp-orch.db"))
	if err != nil {
		log.Fatalf("数据库初始化失败: %v", err)
	}
	defer st.Close()

	verifyURL := func(i *store.Instance) string {
		return cfg.VerifyBaseURL + "/verify/" + i.ID
	}
	orch, err := dockerpkg.New(dockerpkg.Options{
		Host:      cfg.DockerHost,
		Image:     cfg.DERPImage,
		Network:   cfg.DockerNetwork,
		CertsDir:  cfg.CertsDir,
		VerifyURL: verifyURL,
	}, st)
	if err != nil {
		log.Fatalf("Docker 客户端初始化失败: %v", err)
	}

	// Docker 不可达时降级继续(verify 端点仍可用,编排操作会在使用时报错)。
	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := orch.Ping(pingCtx); err != nil {
		log.Printf("警告: Docker daemon 不可达(%v),编排操作将失败", err)
	}
	cancel()

	sweep := lifecycle.New(st, orch, cfg.SweepInterval)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// 证书管理:启动后首次签发(若缺) + 后台续期。续期后回调:重启所有 derper 容器(进程内缓存证书)。
	certEmail := cfg.CertEmail
	if certEmail == "" {
		certEmail = "admin@" + cfg.RelayBaseDomain
	}
	certMgr := cert.New(cert.Options{
		Domain:        "relay." + cfg.RelayBaseDomain,
		WildcardBase:  "relay",
		Email:         certEmail,
		Provider:      cfg.CertDNSProvider,
		Creds:         map[string]string{"DNSPOD_API_ID": cfg.CertDNSPodID, "DNSPOD_API_TOKEN": cfg.CertDNSPodToken},
		CertsDir:      cfg.CertsDir,
		RenewBefore:   time.Duration(cfg.CertRenewBeforeDays) * 24 * time.Hour,
	})
	certMgr.OnRenew = func() error {
		// 重启所有 derper 容器(进程内缓存证书,必须重启才能加载新证书)。
		if out, err := exec.Command("docker", "ps", "--filter", "label=app=derp-orch", "-q").Output(); err == nil {
			if ids := strings.TrimSpace(string(out)); ids != "" {
				_ = exec.Command("docker", append([]string{"restart"}, strings.Fields(ids)...)...).Run()
			}
		}
		return nil
	}
	// cert 管理始终启动;无 DNSPod 凭据时进入被动模式(只消费,外部签/续)。
	go func() {
		if err := certMgr.Run(ctx); err != nil {
			log.Printf("[cert] 证书管理退出: %v", err)
		}
	}()
	if cfg.CertDNSPodID != "" && cfg.CertDNSPodToken != "" {
		log.Printf("cert 管理已启动(主动模式),域名 relay.%s,provider=%s", cfg.RelayBaseDomain, cfg.CertDNSProvider)
	} else {
		log.Printf("cert 管理已启动(被动模式),无凭据;证书由外部(BT 面板)签发后放到 %s", cfg.CertsDir)
	}

	go sweep.Run(ctx)
	log.Printf("sweeper 已启动,间隔 %s", cfg.SweepInterval)

	srv := &http.Server{
		Addr:    cfg.ListenAddr,
		Handler: api.New(cfg, st, orch, sweep),
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP 关闭异常: %v", err)
		}
	}()

	log.Printf("platformd 监听 %s(域名 %s)", cfg.ListenAddr, cfg.RelayBaseDomain)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP 服务异常退出: %v", err)
	}
}

func strEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
