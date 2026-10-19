package admin

import (
	"context"
	"fmt"
	"gag/internal/config"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Deps 是管理面依赖。管理端口只做运维与控制面操作，不参与业务转发，
// 因此依赖全部以接口/回调注入，避免 admin 反向依赖 server 造成循环。
type Deps struct {
	// Metrics 是 Prometheus 文本输出；为 nil 时 /metrics 返回占位内容。
	Metrics http.Handler
	// Ready 返回 nil 表示可以接收流量；为 nil 时视为就绪。
	Ready func() error
	// Store 是配置仓库，/admin/config 与 /admin/rollback 依赖它。
	Store *Store
	// Token 是 /admin/* 的 Bearer 凭证；为空时只允许回环地址访问。
	Token string
	// Info 返回运行时摘要（版本、路由数等），由数据面注入。
	Info func() map[string]any
}

type AdminServer struct {
	cfg  *config.AdminConfig
	deps Deps
	gin  *gin.Engine
	srv  *http.Server
}

func (admin *AdminServer) Initialize() error {
	admin.registerRouters()
	return nil
}

func (admin *AdminServer) Start() error {
	if err := admin.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (admin *AdminServer) Close() error {
	if admin.srv == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(admin.cfg.CloseTimeout)*time.Second)
	defer cancel()

	return admin.srv.Shutdown(ctx)
}

// Handler 暴露完整的 http.Handler，便于测试与复用。
func (admin *AdminServer) Handler() http.Handler { return admin.gin }

// Addr 返回监听地址。
func (admin *AdminServer) Addr() string { return admin.srv.Addr }

func (admin *AdminServer) registerGinGet(path string, h func(ctx *gin.Context)) {
	admin.gin.GET(path, h)
}

func (admin *AdminServer) registerGinPost(path string, h func(ctx *gin.Context)) {
	admin.gin.POST(path, h)
}

func ensureAdminConfig(cfg *config.AdminConfig) {
	if cfg.Mode == "" {
		cfg.Mode = gin.ReleaseMode
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 10
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = 30
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 60
	}
	if cfg.CloseTimeout <= 0 {
		cfg.CloseTimeout = 10
	}
	if cfg.MaxHeaderSize <= 0 {
		cfg.MaxHeaderSize = 1 << 20
	}
	if cfg.MaxBodySize <= 0 {
		cfg.MaxBodySize = 1 << 20
	}
}

func NewAdmin(cfg *config.AdminConfig) *AdminServer {
	return NewAdminWith(cfg, Deps{})
}

func NewAdminWith(cfg *config.AdminConfig, deps Deps) *AdminServer {
	ensureAdminConfig(cfg)

	// 1. *gin.Engine
	engine := newGinEngine(cfg)

	// 2. http.Server
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	httpSrv := &http.Server{
		Addr:           addr,
		Handler:        engine,
		ReadTimeout:    time.Duration(cfg.ReadTimeout) * time.Second,
		WriteTimeout:   time.Duration(cfg.WriteTimeout) * time.Second,
		IdleTimeout:    time.Duration(cfg.IdleTimeout) * time.Second,
		MaxHeaderBytes: cfg.MaxHeaderSize,
	}

	return &AdminServer{
		cfg:  cfg,
		deps: deps,
		gin:  engine,
		srv:  httpSrv,
	}
}

func newGinEngine(cfg *config.AdminConfig) *gin.Engine {
	gin.SetMode(cfg.Mode)
	engine := gin.New()
	engine.Use(gin.Logger())   // 日志
	engine.Use(gin.Recovery()) // 崩溃恢复
	engine.RedirectTrailingSlash = true
	engine.RedirectFixedPath = false
	engine.HandleMethodNotAllowed = true
	engine.ForwardedByClientIP = true
	// 设置最大内存限制
	if cfg.MaxBodySize > 0 {
		engine.MaxMultipartMemory = cfg.MaxBodySize
		engine.Use(func(c *gin.Context) {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, cfg.MaxBodySize)
			c.Next()
		})
	}

	return engine
}
