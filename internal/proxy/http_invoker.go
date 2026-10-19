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

// HttpInvoker 基于 net/http 实现下游调用。
type HttpInvoker struct {
	client   *http.Client
	protocol string
}

// NewHTTPInvoker 创建 HTTP 调用器。
func NewHTTPInvoker() *HttpInvoker {
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
	return &HttpInvoker{client: &http.Client{Transport: transport}, protocol: "http"}
}

func (inv *HttpInvoker) Protocol() string { return inv.protocol }

func (inv *HttpInvoker) InvokeWithCtx(ctx context.Context, req *InvocationRequest) (*InvocationResponse, error) {
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

var _ Invoker = (*HttpInvoker)(nil)

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
