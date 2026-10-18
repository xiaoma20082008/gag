// Package server 实现数据平面（Gag Server）：业务流量经治理链转发到下游。
//
// 与 internal/admin（Gag Admin：控制面接口 + 运维端点）一一对应：
// 两个服务各自持有独立的 http.Server，分别监听业务端口与管理端口。
//
// 业务端口不用任何 Web 框架，直接基于标准库 net.Conn：
// 监听器由 net.Listen 提供，http.Server 从它上面 accept 原生连接，
// 请求进入后立刻交给 gateway pipeline。路由、认证、限流、转发都不经过框架，
// 避免框架的 404/405/301 语义与网关治理链打架。
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"gag/internal/config"
	"gag/internal/proxy"
	"gag/internal/router"
	"gag/internal/telemetry/logging"
	"gag/internal/telemetry/metrics"
)

// GagServer 是网关业务端口的 HTTP 服务。
type GagServer struct {
	cfg     *config.ServerConfig
	addr    string
	srv     *http.Server
	handler http.Handler

	// mu 保护 ln：监听器在 Start 中创建，Addr 可能在其它 goroutine 读取。
	mu sync.Mutex
	ln net.Listener

	// applyMu 串行化配置应用，避免并发发布把新旧 State 交叉组合。
	applyMu sync.Mutex
	state   atomic.Pointer[State]
	// chain 与 state 成对替换：请求要么看到旧的完整组合，要么看到新的。
	chain atomic.Pointer[router.Chain]

	Logger   *logging.Logger
	Metrics  *metrics.Gateway
	Invokers *proxy.Registry
}

func NewGag(cfg *config.ServerConfig) *GagServer {
	if cfg.ReadHeaderTimeout <= 0 {
		cfg.ReadHeaderTimeout = 10 * time.Second
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = 90 * time.Second
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = 15 * time.Second
	}

	s := &GagServer{
		cfg:  cfg,
		addr: fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
	}
	s.handler = &chainHandler{srv: s}
	s.srv = &http.Server{
		Addr:              s.addr,
		Handler:           s.handler,
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		MaxHeaderBytes:    cfg.MaxHeaderSize,
	}
	return s
}

func (s *GagServer) Apply(snap *config.Snapshot) error {
	if snap == nil {
		return errors.New("server: snapshot 为 nil")
	}

	s.applyMu.Lock()
	defer s.applyMu.Unlock()

	prev := s.state.Load()
	state, err := NewState(snap, prev)
	if err != nil {
		return err
	}

	serverCfg := snap.Config().Server
	chain, err := router.Build(router.Options{
		State:    state,
		Logger:   s.Logger,
		Metrics:  s.Metrics,
		MaxBody:  serverCfg.MaxBodyBytes,
		IPAllow:  serverCfg.IPAllow,
		IPDeny:   serverCfg.IPDeny,
		CORS:     serverCfg.CORS,
		Invokers: s.Invokers,
		Source:   state.Source(),
	})
	if err != nil {
		return err
	}

	s.state.Store(state)
	s.chain.Store(chain)
	return nil
}

// Close 优雅停机：先停止接收新连接，再等待在途请求完成。
func (s *GagServer) Close() error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()
	err := s.srv.Shutdown(ctx)

	// Shutdown 会关闭 Serve 正在使用的监听器；从未启动或 Serve 尚未接管时
	// 需要在这里兜底关闭，否则端口会一直占着。
	s.mu.Lock()
	ln := s.ln
	s.mu.Unlock()
	if ln != nil {
		if cerr := ln.Close(); cerr != nil && !errors.Is(cerr, net.ErrClosed) {
			return errors.Join(err, cerr)
		}
	}
	return err
}

// Handler 暴露完整的 http.Handler，便于测试与复用。
func (s *GagServer) Handler() http.Handler { return s.handler }

// Addr 返回实际监听地址；未启动时为配置地址（端口可能为 0）。
func (s *GagServer) Addr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln != nil {
		return s.ln.Addr().String()
	}
	return s.addr
}

// Listener 返回原生监听器，便于测试或复用同一端口。
func (s *GagServer) Listener() net.Listener {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ln
}

// State 返回当前生效的运行时视图；未应用配置时为 nil。
func (s *GagServer) State() *State { return s.state.Load() }

// Chain 返回当前生效的治理链；未应用配置时为 nil。
func (s *GagServer) Chain() *router.Chain { return s.chain.Load() }
