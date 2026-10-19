package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gag/internal/config"
	"gag/internal/telemetry/logging"
)

const baseConfig = `
version: v1
server:
  listen: "127.0.0.1:18080"
  admin_listen: "127.0.0.1:19090"
  default_tenant: t1
  discovery:
    type: static
    instances:
      - tenant: t1
        service: svc
        cluster: c1
        addr: 127.0.0.1:1
tenants:
  - name: t1
    services:
      - name: svc
        protocol: http
        timeout: 200ms
        routes:
          - id: r1
            path: /v1/orders
            cluster: c1
            auth:
              type: api_key
              keys: ["k1"]
        clusters:
          - name: c1
`

func newTestServer(t *testing.T, yaml string) (*GagServer, *config.Snapshot) {
	t.Helper()
	return newTestServerOn(t, yaml, 18080)
}

func newTestServerOn(t *testing.T, yaml string, port int) (*GagServer, *config.Snapshot) {
	t.Helper()

	cfg, err := config.Load(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("加载配置: %v", err)
	}
	snap, err := config.NewSnapshot(cfg)
	if err != nil {
		t.Fatalf("构建快照: %v", err)
	}

	logger := logging.New(logging.Config{Level: logging.LevelError})
	t.Cleanup(func() { _ = logger.Close() })

	s := NewGag(&config.ServerConfig{Host: "127.0.0.1", Port: port})
	return s, snap
}

func do(s *GagServer, method, path string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestGagServerRequiresConfig(t *testing.T) {
	s, _ := newTestServer(t, baseConfig)
	if err := s.Start(); err == nil {
		t.Fatal("未应用配置时不应允许启动")
	}
	if err := s.Apply(nil); err == nil {
		t.Fatal("Apply(nil) 应返回错误")
	}
}

func TestGagServerServesChain(t *testing.T) {
	s, snap := newTestServer(t, baseConfig)
	if err := s.Apply(snap); err != nil {
		t.Fatalf("应用配置: %v", err)
	}

	if got := do(s, http.MethodGet, "/v1/orders", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("缺少凭证应为 401，实际 %d: %s", got.Code, got.Body.String())
	}

	if got := do(s, http.MethodGet, "/nope", map[string]string{"X-API-Key": "k1"}); got.Code != http.StatusNotFound {
		t.Fatalf("未知路由应为 404，实际 %d", got.Code)
	}

	// 下游不可达时应是 502，而不是被 gin 的默认 404 吞掉。
	if got := do(s, http.MethodGet, "/v1/orders", map[string]string{"X-API-Key": "k1"}); got.Code != http.StatusBadGateway {
		t.Fatalf("下游不可达应为 502，实际 %d: %s", got.Code, got.Body.String())
	}

	// 业务端口不应暴露管理端点。
	if got := do(s, http.MethodGet, "/healthz", nil); got.Code != http.StatusNotFound {
		t.Fatalf("业务端口不应响应管理端点，实际 %d", got.Code)
	}
}

func TestGagServerReloadKeepsPolicyState(t *testing.T) {
	s, snap := newTestServer(t, baseConfig)
	if err := s.Apply(snap); err != nil {
		t.Fatalf("首次应用: %v", err)
	}
	first := s.State()

	if err := s.Apply(snap); err != nil {
		t.Fatalf("重复应用: %v", err)
	}
	second := s.State()
	if second.ReusedPolicies() != second.RouteCount() {
		t.Fatalf("配置未变时应复用全部策略，复用 %d/%d", second.ReusedPolicies(), second.RouteCount())
	}
	if first.Policy("r1") != second.Policy("r1") {
		t.Fatal("配置未变时不应重建 policy.Set（否则限流令牌与熔断计数会丢失）")
	}

	// 删除路由后应立刻对新配置生效。
	trimmed := strings.Replace(baseConfig, `          - id: r1
            path: /v1/orders
            cluster: c1
            auth:
              type: api_key
              keys: ["k1"]
`, `          - id: r2
            path: /v1/users
            cluster: c1
`, 1)
	cfg, err := config.Load(strings.NewReader(trimmed))
	if err != nil {
		t.Fatalf("加载新配置: %v", err)
	}
	next, err := config.NewSnapshot(cfg)
	if err != nil {
		t.Fatalf("构建新快照: %v", err)
	}
	if err := s.Apply(next); err != nil {
		t.Fatalf("应用新配置: %v", err)
	}
	if got := do(s, http.MethodGet, "/v1/orders", nil); got.Code != http.StatusNotFound {
		t.Fatalf("旧路由应失效，实际 %d", got.Code)
	}
	if got := s.State().Policy("r2"); got == nil {
		t.Fatal("新路由策略应存在")
	}
}

func TestGagServerHandlerIsStable(t *testing.T) {
	s, snap := newTestServer(t, baseConfig)
	if err := s.Apply(snap); err != nil {
		t.Fatalf("应用配置: %v", err)
	}
	h := s.Handler()
	if err := s.Apply(snap); err != nil {
		t.Fatalf("重新应用: %v", err)
	}
	if s.Handler() != h {
		t.Fatal("热加载不应替换 http.Handler，否则监听中的连接会指向旧实例")
	}
}

// TestGagServerServesOverNativeConn 直接用 net.Conn 发原始 HTTP 请求，
// 验证业务端口不依赖任何 Web 框架：监听、accept、读写都在标准库这一层。
func TestGagServerServesOverNativeConn(t *testing.T) {
	s, snap := newTestServerOn(t, baseConfig, 0)
	if err := s.Apply(snap); err != nil {
		t.Fatalf("应用配置: %v", err)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- s.Start() }()
	t.Cleanup(func() {
		_ = s.Close()
		<-errCh
	})

	addr := waitForListen(t, s)
	conn, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("连接 %s: %v", addr, err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := fmt.Fprintf(conn, "GET /v1/orders HTTP/1.1\r\nHost: gw\r\nConnection: close\r\n\r\n"); err != nil {
		t.Fatalf("写入请求: %v", err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("读取响应: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("原生连接上的请求应被治理链拒绝为 401，实际 %d", resp.StatusCode)
	}
}

// waitForListen 等到监听器就绪并返回真实地址（端口为 0 时尤其需要）。
func waitForListen(t *testing.T, s *GagServer) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ln := s.Listener(); ln != nil {
			return ln.Addr().String()
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("服务未在 2s 内开始监听")
	return ""
}

// nacosConfig 只验证装配路径：注册中心不可达时网关仍应正常装配，
// 实例查不到是调用期的事（502/503），不该阻塞启动。
const nacosConfig = `
version: v1
server:
  listen: "127.0.0.1:18081"
  admin_listen: "127.0.0.1:19091"
  default_tenant: t1
  discovery:
    type: nacos
    address: 127.0.0.1:18848
    namespace: test
    log_level: warn
    timeout_ms: 200
tenants:
  - name: t1
    services:
      - name: svc
        protocol: http
        timeout: 200ms
        routes:
          - id: r1
            path: /v1/orders
            cluster: c1
        clusters:
          - name: c1
`

func TestApplyWithNacosDiscovery(t *testing.T) {
	s, snap := newTestServer(t, nacosConfig)
	if err := s.Apply(snap); err != nil {
		t.Fatalf("nacos 发现配置应能装配: %v", err)
	}
	if s.State().Table() == nil {
		t.Fatal("装配后查找表不应为空")
	}
}

func TestApplyRejectsUnsupportedDiscoveryType(t *testing.T) {
	broken := strings.Replace(nacosConfig, "type: nacos", "type: consul", 1)
	s, snap := newTestServer(t, broken)
	err := s.Apply(snap)
	if err == nil {
		t.Fatal("consul 适配器尚未实现，Apply 应失败")
	}
	if !strings.Contains(err.Error(), "consul") {
		t.Fatalf("错误信息应指出未实现的类型: %v", err)
	}
}
