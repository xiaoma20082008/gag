// Package proxy 封装下游调用，屏蔽 HTTP/gRPC/Dubbo 等协议差异。
package proxy

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// 调用层错误。
var (
	// ErrTransport 表示连接失败、超时等传输层问题，通常可以重试。
	ErrTransport = errors.New("proxy: transport failure")
	// ErrUnsupported 表示当前协议尚未实现。
	ErrUnsupported = errors.New("proxy: protocol not supported")
)

// InvocationRequest 是一次下游调用。
type InvocationRequest struct {
	Method  string
	URL     string
	Header  http.Header
	Body    []byte
	Timeout time.Duration
}

// InvocationResponse 是下游返回。
type InvocationResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Latency    time.Duration
	Upstream   string
}

// Invoker 是协议适配器。
type Invoker interface {
	Protocol() string
	InvokeWithCtx(ctx context.Context, req *InvocationRequest) (*InvocationResponse, error)
}

// Registry 保存各协议的 invoker。
type Registry struct {
	invokers map[string]Invoker
}

// NewRegistry 创建注册表，默认注册 HTTP 调用器。
func NewRegistry() *Registry {
	r := &Registry{invokers: make(map[string]Invoker, 4)}
	r.Register(NewHTTPInvoker())
	return r
}

// Register 注册协议实现。
func (r *Registry) Register(inv Invoker) {
	if inv == nil {
		return
	}
	r.invokers[strings.ToLower(inv.Protocol())] = inv
}

// Get 返回协议对应的调用器；未注册时返回 nil。
func (r *Registry) Get(protocol string) Invoker {
	if protocol == "" {
		protocol = "http"
	}
	return r.invokers[strings.ToLower(protocol)]
}

// Protocols 返回已注册协议，便于启动日志。
func (r *Registry) Protocols() []string {
	out := make([]string, 0, len(r.invokers))
	for k := range r.invokers {
		out = append(out, k)
	}
	return out
}
