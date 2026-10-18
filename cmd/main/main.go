package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"

	"github.com/gin-gonic/gin"

	"gag/internal/admin"
	"gag/internal/config"
	"gag/internal/proxy"
	"gag/internal/router"
	"gag/internal/server"
	"gag/internal/telemetry/logging"
	"gag/internal/telemetry/metrics"
)

// 构建期由 scripts/build.sh 通过 -ldflags "-X main.version=..." 注入。
// 只用于日志与排障定位，不参与任何业务判断。
var (
	version   = "dev"
	commit    = "unknown"
	buildTime = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gateway 启动失败:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "configs/gateway.yaml", "配置文件路径")
	flag.Parse()

	logger := logging.New(logging.Config{Level: logging.LevelInfo, Async: true})
	defer func() { _ = logger.Close() }()

	// 按扩展名选 loader（.yaml/.yml/.json）：格式差异封装在 config 包里，
	// 这里的启动流程不关心配置是哪种编码。
	loader, err := config.NewLoader(*configPath)
	if err != nil {
		logger.Error("无法确定配置格式", map[string]any{"path": *configPath, "error": err.Error()})
		return err
	}
	cfg, err := loader.Load(*configPath)
	if err != nil {
		logger.Error("加载配置失败", map[string]any{"path": *configPath, "error": err.Error()})
		return err
	}
	snap, err := config.NewSnapshot(cfg)
	if err != nil {
		return err
	}

	registry := metrics.NewRegistry()
	gatewayMetrics := metrics.NewGateway(registry)

	store := admin.NewStore()
	if _, err := store.PublishSnapshot(snap, "bootstrap", "initial load"); err != nil {
		return err
	}

	invokers := proxy.NewRegistry()

	// ---------- 数据平面：业务端口 ----------
	gagHost, gagPort, err := splitListen(cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("server.listen: %w", err)
	}
	gag := server.NewGag(&server.ServerConfig{
		Host:              gagHost,
		Port:              gagPort,
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout.Std(),
		IdleTimeout:       cfg.Server.IdleTimeout.Std(),
		ShutdownTimeout:   cfg.Server.ShutdownTimeout.Std(),
	}, server.Deps{
		Logger:   logger,
		Metrics:  gatewayMetrics,
		Invokers: invokers,
	})
	if err := gag.Apply(snap); err != nil {
		return err
	}

	// ---------- 控制平面：管理端口 ----------
	adminHost, adminPort, err := splitListen(cfg.Server.AdminListen)
	if err != nil {
		return fmt.Errorf("server.admin_listen: %w", err)
	}
	adminSrv := admin.NewAdminWith(&admin.AdminConfig{
		Mode: gin.ReleaseMode,
		Host: adminHost,
		Port: adminPort,
	}, admin.Deps{
		Metrics: registry,
		Ready: func() error {
			if gag.State() == nil {
				return errors.New("配置尚未装载")
			}
			return nil
		},
		Store: store,
		Token: cfg.Server.AdminToken,
		Info: func() map[string]any {
			st := gag.State()
			if st == nil {
				return map[string]any{"data_plane": "starting"}
			}
			return map[string]any{
				"data_plane": "ready",
				"listen":     gag.Addr(),
				"routes":     st.RouteCount(),
				"tenants":    st.TenantCount(),
			}
		},
	})
	if err := adminSrv.Initialize(); err != nil {
		return err
	}

	logger.Info("治理链已装配", map[string]any{
		"topology":     router.Topology(gag.Chain()),
		"listen":       gag.Addr(),
		"admin_listen": adminSrv.Addr(),
		"build":        version + " (" + commit + ")",
		"build_time":   buildTime,
	})

	// ---------- 热加载：控制面发布 -> 数据面原子替换 ----------
	watchCtx, stopWatch := context.WithCancel(context.Background())
	defer stopWatch()
	updates := store.Watch(watchCtx)
	var watchWG sync.WaitGroup
	watchWG.Add(1)
	go func() {
		defer watchWG.Done()
		for {
			var next *config.Snapshot
			select {
			case <-watchCtx.Done():
				// Store 刻意不关闭 channel（避免与发布方竞争），
				// 因此退出条件只能由消费方从 ctx 感知。
				return
			case next = <-updates:
			}
			if next == nil {
				continue
			}
			if err := gag.Apply(next); err != nil {
				// 新配置不可用时保留旧配置：宁可跑旧版本，也不要半可用。
				logger.Error("热加载失败，保留当前配置", map[string]any{
					"version": next.Version(),
					"error":   err.Error(),
				})
				gatewayMetrics.ConfigApply.WithLabelValues("error").Inc()
				continue
			}
			st := gag.State()
			logger.Info("配置已热加载", map[string]any{
				"version": next.Version(),
				"hash":    next.Hash()[:12],
				"routes":  st.RouteCount(),
				"reused":  st.ReusedPolicies(),
			})
			gatewayMetrics.ConfigApply.WithLabelValues("ok").Inc()
		}
	}()

	// ---------- 双端口同时监听 ----------
	errCh := make(chan error, 2)
	go func() { errCh <- gag.Start() }()
	go func() { errCh <- adminSrv.Start() }()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var runErr error
	select {
	case sig := <-sigCh:
		logger.Info("收到退出信号，开始优雅停机", map[string]any{"signal": sig.String()})
	case err := <-errCh:
		if err != nil {
			logger.Error("监听异常退出", map[string]any{"error": err.Error()})
			runErr = err
		}
	}

	// 先停热加载，再停监听，避免停机过程中还在替换配置。
	stopWatch()
	watchWG.Wait()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout.Std())
	defer cancel()
	if err := shutdown(shutdownCtx, gag, adminSrv); err != nil {
		logger.Error("优雅停机失败", map[string]any{"error": err.Error()})
		if runErr == nil {
			runErr = err
		}
		return runErr
	}

	logger.Info("网关已停止", map[string]any{"version": snap.Version()})
	return runErr
}

// splitListen 把 "host:port" 拆成 http.Server 需要的 host 与端口。
func splitListen(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("%q 不是合法的 host:port: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 0 || port > 65535 {
		return "", 0, fmt.Errorf("%q 的端口非法", addr)
	}
	return host, port, nil
}

// shutdown 并行关闭两个端口：它们互不依赖，串行关闭只会拉长停机时间。
func shutdown(ctx context.Context, gag *server.GagServer, adminSrv *admin.AdminServer) error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	collect := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}

	wg.Add(2)
	go func() { defer wg.Done(); collect(gag.Close()) }()
	go func() { defer wg.Done(); collect(adminSrv.Close()) }()

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		return errors.Join(errs...)
	case <-ctx.Done():
		return fmt.Errorf("停机超时: %w", ctx.Err())
	}
}
