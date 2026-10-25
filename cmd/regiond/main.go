// Command regiond 在地区宿主机上运行,代理 platformd 的容器编排请求。
// 只暴露 HTTPS + Token 的窄接口,不需要把 Docker API 暴露到公网。
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"derp-orch/internal/agent"
	"derp-orch/internal/docker"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[regiond] ")

	token := os.Getenv("REGION_AGENT_TOKEN")
	if token == "" {
		log.Fatal("REGION_AGENT_TOKEN 必填(控制面访问本代理的 Bearer Token)")
	}

	orch, err := docker.New(docker.Options{
		Host:    strEnv("DOCKER_HOST", "unix:///var/run/docker.sock"),
		Image:   strEnv("DERP_IMAGE", "shadowofgost/derper:main-2026-08-15"),
		Network: strEnv("DOCKER_NETWORK", "bridge"),
	}, nil)
	if err != nil {
		log.Fatalf("创建编排器: %v", err)
	}
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 3*time.Second)
	if err := orch.Ping(pingCtx); err != nil {
		log.Printf("警告: Docker daemon 不可达(稍后仍会重试): %v", err)
	}
	cancelPing()

	srv := &http.Server{
		Addr:              strEnv("LISTEN_ADDR", ":8081"),
		Handler:           agent.NewServer(orch, token).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	certFile, keyFile := os.Getenv("AGENT_TLS_CERT"), os.Getenv("AGENT_TLS_KEY")
	log.Printf("regiond 监听 %s(TLS=%v)", srv.Addr, certFile != "" && keyFile != "")
	if certFile != "" && keyFile != "" {
		err = srv.ListenAndServeTLS(certFile, keyFile)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("regiond 退出: %v", err)
	}
}

func strEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
