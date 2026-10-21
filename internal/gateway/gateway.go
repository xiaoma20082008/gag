package gateway

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"gag/internal/admin"
	"gag/internal/config"
	"gag/internal/discovery"
	"gag/internal/proxy"
	"gag/internal/router"
	"gag/internal/script"
	"gag/internal/server"
	"gag/internal/telemetry/logging"
	"gag/internal/telemetry/metrics"
)

const defaultShutdownTimeout = 15 * time.Second
const AppId = "gag"

// Gateway 网关运行时
type Gateway struct {
	cfg  *config.Config
	snap *config.Snapshot

	logger   *logging.Logger
	registry *metrics.Registry
	metrics  *metrics.Gateway
	store    *admin.Store
	invokers *proxy.Registry

	// cc 配置中心客户端
	cc  config.ConfigClient
	ccc *config.ConfigClientConfig

	// dc 注册中心客户端
	dc  discovery.DiscoveryClient
	dcc *config.DiscoveryClientConfig

	manager *script.ScriptManager

	// Data 数据平面
	Data *server.GagServer
	// Admin 控制平面
	Admin *admin.AdminServer

	errCh chan error

	watchCancel context.CancelFunc
	watchWG     sync.WaitGroup

	started   bool
	closeOnce sync.Once
	closeErr  error
}

func New(ccc *config.ConfigClientConfig) (*Gateway, error) {
	g := &Gateway{
		ccc: ccc,
	}
	if err := g.Initialize(); err != nil {
		_ = g.Close()
		return nil, err
	}
	return g, nil
}

// Initialize 初始化网关的配置和相关参数
func (g *Gateway) Initialize() error {
	if g.Data != nil || g.Admin != nil {
		return errors.New("gateway: 已装配，不能重复 Initialize")
	}
	cc, err := config.NewConfigClient(g.ccc)
	if err != nil {
		return err
	}
	g.cc = cc
	if err := g.loadConfig(); err != nil {
		return err
	}
	g.store = admin.NewStore()
	if _, err := g.store.PublishSnapshot(g.snap, "bootstrap", "initial load"); err != nil {
		return fmt.Errorf("初始化配置仓库: %w", err)
	}

	g.logger = logging.New(logging.Config{
		Level: logging.LevelInfo,
		Async: true,
	})
	g.registry = metrics.NewRegistry()
	g.invokers = proxy.NewRegistry()
	g.metrics = metrics.NewGateway(g.registry)

	dc, err := discovery.NewDiscoveryClient(nil)
	if err != nil {
		return err
	}
	g.dc = dc

	g.manager = script.NewScriptManager()

	if err := g.assemble(); err != nil {
		return err
	}
	g.logger.Info("治理链已装配", map[string]any{
		"topology":     router.Topology(g.Data.Chain()),
		"listen":       g.Data.Addr(),
		"admin_listen": g.Admin.Addr(),
		"version":      g.snap.Version(),
		"routes":       g.Data.State().RouteCount(),
		"build":        BuildInfo().String(),
	})
	return nil
}

func (g *Gateway) loadConfig() error {
	raw := g.cc.Get(context.Background(), AppId, "")
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("config: %s 客户端没有返回内容（key=%q）", g.cc.Type(), AppId)
	}
	snap, err := config.SnapshotFrom([]byte(raw), AppId, "")
	if err != nil {
		g.logger.Error("加载配置失败", map[string]any{
			"key":   AppId,
			"error": err.Error(),
		})
		return err
	}
	g.cfg, g.snap = snap.Config(), snap
	return nil
}

// assemble 创建并连接两个端口所需的全部组件。
func (g *Gateway) assemble() error {

	if err := g.startDataPlane(); err != nil {
		return err
	}
	return g.startAdminPlane()
}

func (g *Gateway) startDataPlane() error {
	host, port, err := splitListen(g.cfg.Server.Listen)
	if err != nil {
		return fmt.Errorf("server.listen: %w", err)
	}

	g.Data = server.NewGag(&config.ServerConfig{
		Host:              host,
		Port:              port,
		ReadHeaderTimeout: g.cfg.Server.ReadHeaderTimeout.Std(),
		IdleTimeout:       g.cfg.Server.IdleTimeout.Std(),
		ShutdownTimeout:   g.cfg.Server.ShutdownTimeout.Std(),
	})

	if err := g.Data.Apply(g.snap); err != nil {
		return fmt.Errorf("装载初始配置: %w", err)
	}
	return nil
}

func (g *Gateway) startAdminPlane() error {
	host, port, err := splitListen(g.cfg.Server.AdminListen)
	if err != nil {
		return fmt.Errorf("server.admin_listen: %w", err)
	}

	g.Admin = admin.NewAdminWith(&config.AdminConfig{
		Mode: gin.ReleaseMode,
		Host: host,
		Port: port,
	}, admin.Deps{
		Metrics: g.registry,
		Ready: func() error {
			if g.Data.State() == nil {
				return errors.New("配置尚未装载")
			}
			return nil
		},
		Store: g.store,
		Token: g.cfg.Server.AdminToken,
		Info:  g.info,
	})
	return g.Admin.Initialize()
}

// info 是 /admin/status 的运行时摘要。
func (g *Gateway) info() map[string]any {
	st := g.Data.State()
	if st == nil {
		return map[string]any{"data_plane": "starting"}
	}
	return map[string]any{
		"data_plane": "ready",
		"listen":     g.Data.Addr(),
		"routes":     st.RouteCount(),
		"tenants":    st.TenantCount(),
	}
}

func (g *Gateway) Start() error {
	if g.started {
		return errors.New("gateway: 已启动")
	}
	if g.Data == nil || g.Admin == nil {
		return errors.New("gateway: 尚未装配，先调用 New")
	}
	g.started = true
	g.errCh = make(chan error, 2)

	go func() { g.errCh <- g.Data.Start() }()
	go func() { g.errCh <- g.Admin.Start() }()
	return nil
}

func (g *Gateway) Wait(ctx context.Context) error {
	if g.errCh == nil {
		return errors.New("gateway: 尚未启动")
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-g.errCh:
		if err != nil {
			g.logger.Error("监听异常退出", map[string]any{"error": err.Error()})
		}
		return err
	}
}

// Run 启动、阻塞、优雅停机。
func (g *Gateway) Run(ctx context.Context) error {
	if err := g.Start(); err != nil {
		return err
	}
	runErr := g.Wait(ctx)
	return errors.Join(runErr, g.Close())
}

// Close 优雅停机：先停热加载，再并行关闭两个端口。
func (g *Gateway) Close() error {
	g.closeOnce.Do(func() {
		// 1. 关闭配置。
		if g.cc != nil {
			// 等轮询 goroutine 真正退出，避免它回调到已停的数据面。
			_ = g.cc.Close()
		}
		// 2. 关闭服务。
		ctx, cancel := context.WithTimeout(context.Background(), g.shutdownTimeout())
		defer cancel()
		// 3.
		g.closeErr = shutdown(ctx, g.Data, g.Admin)
		g.logger.Info("网关已停止", map[string]any{"version": g.snap.Version()})
		_ = g.logger.Close()
	})
	return g.closeErr
}

func (g *Gateway) shutdownTimeout() time.Duration {
	if g.cfg == nil {
		return defaultShutdownTimeout
	}
	if d := g.cfg.Server.ShutdownTimeout.Std(); d > 0 {
		return d
	}
	return defaultShutdownTimeout
}

// Snapshot 返回启动时装载的配置快照。
func (g *Gateway) Snapshot() *config.Snapshot { return g.snap }

// Logger 返回网关的日志器，供入口层记录信号之类的进程事件。
func (g *Gateway) Logger() *logging.Logger { return g.logger }
