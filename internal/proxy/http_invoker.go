package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// HTTPInvoker 基于 net/http 实现下游调用。
//
// 连接池由共享 Transport 提供：网关是典型的"多下游、高复用"场景，
// 每次请求新建连接会显著放大尾延迟。
type HTTPInvoker struct {
	client   *http.Client
	protocol string
}

// NewHTTPInvoker 创建 HTTP 调用器。
func NewHTTPInvoker() *HTTPInvoker { return NewHTTPInvokerAs("http") }

// NewHTTPInvokerAs 以指定协议名注册 HTTP 调用器。
//
// AI 推理服务通常也是 HTTP/SSE 接口，只是超时与并发模型不同，
// 因此复用同一实现但使用独立协议名，便于按协议替换为专用客户端。
func NewHTTPInvokerAs(protocol string) *HTTPInvoker {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   3 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          1024,
		MaxIdleConnsPerHost:   128,
		MaxConnsPerHost:       0,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   3 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
	}
	return &HTTPInvoker{client: &http.Client{Transport: transport}, protocol: protocol}
}

func (inv *HTTPInvoker) Protocol() string { return inv.protocol }

// InvokeWithCtx 执行一次调用；超时由调用方通过 ctx 或 req.Timeout 控制。
func (inv *HTTPInvoker) InvokeWithCtx(ctx context.Context, req *InvocationRequest) (*InvocationResponse, error) {
	if req == nil || req.URL == "" {
		return nil, fmt.Errorf("%w: 空 URL", ErrTransport)
	}
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	for k, vs := range req.Header {
		for _, v := range vs {
			httpReq.Header.Add(k, v)
		}
	}

	start := time.Now()
	resp, err := inv.client.Do(httpReq)
	latency := time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTransport, err)
	}
	defer resp.Body.Close()

	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		// 响应体读取失败属于传输层问题；已读到的部分仍然返回，便于排查。
		return &InvocationResponse{
			StatusCode: resp.StatusCode,
			Header:     resp.Header.Clone(),
			Body:       data,
			Latency:    latency,
			Upstream:   req.URL,
		}, fmt.Errorf("%w: 读取响应体: %v", ErrTransport, readErr)
	}

	return &InvocationResponse{
		StatusCode: resp.StatusCode,
		Header:     resp.Header.Clone(),
		Body:       data,
		Latency:    latency,
		Upstream:   req.URL,
	}, nil
}

var _ Invoker = (*HTTPInvoker)(nil)

// IsTimeout 判断错误是否由超时引起。
func IsTimeout(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
