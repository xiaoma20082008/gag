package router

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"gag/internal/config"
	"gag/internal/core"
	"gag/internal/discovery"
	"gag/internal/middleware"
	"gag/internal/proxy"
	"gag/internal/telemetry/logging"
	"gag/internal/telemetry/metrics"
)

// ---------- 测试基建 ----------

// memState 是 State 的内存实现。
//
// 这里必须自带一份而不能复用 server.State：server 依赖 modules 的装配结果，
// 反向 import 会成环。测试因此自己编译路由表与策略集——
// 这也是 State 接口只暴露三个方法的原因，测试 fixture 才不会随实现膨胀。
type memState struct {
	snapshot *config.Snapshot
	table    *Table
	policies map[string]*middleware.Set
	source   discovery.DiscoveryClient
}

func (s *memState) Table() *Table                     { return s.table }
func (s *memState) Source() discovery.DiscoveryClient { return s.source }
func (s *memState) Policy(id string) *middleware.Set  { return s.policies[id] }
func (s *memState) Snapshot() *config.Snapshot        { return s.snapshot }

// newMemState 从 YAML 编译出一份可用的运行时视图。
func newMemState(t *testing.T, yaml string) *memState {
	t.Helper()

	cfg, err := config.Load(strings.NewReader(yaml))
	if err != nil {
		t.Fatalf("加载配置: %v", err)
	}
	snap, err := config.NewSnapshot(cfg)
	if err != nil {
		t.Fatalf("构建快照: %v", err)
	}
	table, err := NewTable(snap)
	if err != nil {
		t.Fatalf("构建路由表: %v", err)
	}
	src, err := discovery.NewDiscoveryClient(cfg.Server.Discovery)
	if err != nil {
		t.Fatalf("构建实例来源: %v", err)
	}

	st := &memState{snapshot: snap, table: table, source: src, policies: map[string]*middleware.Set{}}
	for ti := range cfg.Tenants {
		tenant := &cfg.Tenants[ti]
		for si := range tenant.Services {
			svc := &tenant.Services[si]
			for ri := range svc.Routes {
				route := &svc.Routes[ri]
				set, err := middleware.BuildSet(route.ID, svc, route)
				if err != nil {
					t.Fatalf("构建策略 %s: %v", route.ID, err)
				}
				st.policies[route.ID] = set
			}
		}
	}
	return st
}

// testDownstream 是一个可编程下游：按序返回脚本化的状态码，并记录收到的请求。
type testDownstream struct {
	server *httptest.Server
	hits   atomic.Int32
	// seen 保存最近一次收到的请求头，用于验证透传。
	lastHeader atomic.Value
}

// newTestDownstream 创建一个可编程下游：记录命中次数与最近一次请求头。
func newTestDownstream(handler func(w http.ResponseWriter, r *http.Request)) *testDownstream {
	d := &testDownstream{}
	d.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		d.hits.Add(1)
		d.lastHeader.Store(r.Header.Clone())
		handler(w, r)
	}))
	return d
}

func (d *testDownstream) addr() string {
	return strings.TrimPrefix(d.server.URL, "http://")
}

func (d *testDownstream) close() { d.server.Close() }

func (d *testDownstream) header() http.Header {
	v := d.lastHeader.Load()
	if v == nil {
		return http.Header{}
	}
	return v.(http.Header)
}

// buildChain 装配治理链；mutate 可在装配前改写 Options（例如替换 invoker）。
func buildChain(t *testing.T, yaml string, mutate func(*Options)) *Chain {
	t.Helper()

	registry := metrics.NewRegistry()
	st := newMemState(t, yaml)
	opts := Options{
		// 默认用 Error 级别压掉访问日志噪音；需要断言日志的用例自己改 Logger。
		State:    st,
		Source:   st.Source(),
		Logger:   logging.New(logging.Config{Level: logging.LevelError}),
		Metrics:  metrics.NewGateway(registry),
		MaxBody:  1024,
		Invokers: proxy.NewRegistry(),
	}
	// 入口级策略一律取自配置，与 server.Apply 的做法一致：
	// 辅助函数如果自己拼一份，测的就和线上跑的不是同一条装配路径了。
	opts.CORS = opts.State.Snapshot().Config().Server.CORS
	opts.IPAllow = opts.State.Snapshot().Config().Server.IPAllow
	opts.IPDeny = opts.State.Snapshot().Config().Server.IPDeny
	if mutate != nil {
		mutate(&opts)
	}
	ch, err := Build(opts)
	if err != nil {
		t.Fatalf("装配治理链: %v", err)
	}
	return ch
}

func do(ch *Chain, method, path string, headers map[string]string, body io.Reader) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	ch.ServeHTTP(rec, req)
	return rec
}

func decodeErr(t *testing.T, rec *httptest.ResponseRecorder) map[string]string {
	t.Helper()
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是合法 JSON: %s", rec.Body.String())
	}
	return out
}

// 基础配置：默认租户 t1，GET /v1/orders 需 api_key，POST /v1/orders 不重试。
const downstreamConfig = `
version: v1
server:
  listen: "127.0.0.1:18080"
  admin_listen: "127.0.0.1:19090"
  default_tenant: t1
  max_body_bytes: 1024
  discovery:
    type: static
    instances:
      - tenant: t1
        service: svc
        cluster: c1
        addr: %s
tenants:
  - name: t1
    services:
      - name: svc
        protocol: http
        timeout: 500ms
        retry:
          max_attempts: 3
          backoff: 1ms
          max_backoff: 5ms
          jitter: 0
        routes:
          - id: r_get
            path: /v1/orders
            methods: [GET]
            cluster: c1
            auth:
              type: api_key
              keys: ["k1"]
            rate_limit:
              rps: 1
              burst: 1
          - id: r_post
            path: /v1/orders
            methods: [POST]
            cluster: c1
            auth:
              type: api_key
              keys: ["k1"]
          - id: r_open
            path: /v1/open
            methods: [GET]
            cluster: c1
          - id: r_strict
            path: /v1/strict
            methods: [POST]
            cluster: c1
            auth:
              type: api_key
              keys: ["k1"]
            validate:
              max_body_bytes: 16
              content_types: ["application/json"]
              required_headers: ["X-Trace"]
              required_query: ["q"]
              required_body_fields: ["name"]
        clusters:
          - name: c1
            lb: round_robin
`

func cfgWithDownstream(addr string) string {
	return fmt.Sprintf(downstreamConfig, addr)
}

// ---------- 行为回归：401 / 404 / 502 ----------

func TestChainRejectsUnauthenticated(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer down.close()

	ch := buildChain(t, cfgWithDownstream(down.addr()), nil)

	rec := do(ch, http.MethodGet, "/v1/orders", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("缺少凭证应为 401，实际 %d: %s", rec.Code, rec.Body.String())
	}
	body := decodeErr(t, rec)
	if body["code"] != "unauthenticated" {
		t.Fatalf("错误码 = %q, want unauthenticated", body["code"])
	}
	if body["request_id"] == "" {
		t.Fatal("错误响应必须带上 request_id，否则客户端无法与日志对齐")
	}
	if down.hits.Load() != 0 {
		t.Fatal("认证失败不应打到下游")
	}
}

func TestChainUnknownRouteIs404(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer down.close()

	ch := buildChain(t, cfgWithDownstream(down.addr()), nil)
	rec := do(ch, http.MethodGet, "/v1/nope", map[string]string{"X-API-Key": "k1"}, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("未知路由应为 404，实际 %d", rec.Code)
	}
	if got := decodeErr(t, rec)["code"]; got != "not_found" {
		t.Fatalf("错误码 = %q, want not_found", got)
	}
}

func TestChainUnknownTenantIs403(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer down.close()

	ch := buildChain(t, cfgWithDownstream(down.addr()), nil)
	rec := do(ch, http.MethodGet, "/v1/orders", map[string]string{
		"X-API-Key": "k1",
		"X-Tenant":  "ghost",
	}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("未知租户应为 403，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestChainDownstreamUnreachableIs502 用真实网络失败验证：
// 实例地址不可达时不能是 500，也不能把连接断掉。
func TestChainDownstreamUnreachableIs502(t *testing.T) {
	ch := buildChain(t, cfgWithDownstream("127.0.0.1:1"), nil)

	rec := do(ch, http.MethodGet, "/v1/orders", map[string]string{"X-API-Key": "k1"}, nil)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("下游不可达应为 502，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if got := decodeErr(t, rec)["code"]; got != "bad_gateway" {
		t.Fatalf("错误码 = %q, want bad_gateway", got)
	}
	// 413 字节的 body 说明真的写了错误响应体，而不是空响应。
	if rec.Body.Len() == 0 {
		t.Fatal("502 必须带错误响应体")
	}
}

// ---------- 413 / 400 ----------

func TestChainBodyTooLargeIs413(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer down.close()

	ch := buildChain(t, cfgWithDownstream(down.addr()), nil)

	// 进程级上限 1024，路由级 16：取更紧的那个。
	rec := do(ch, http.MethodPost, "/v1/strict", map[string]string{
		"X-API-Key": "k1",
		"X-Trace":   "t",
		"Query":     "q=1",
	}, bytes.NewBufferString(`{"name":"`+strings.Repeat("x", 64)+`"}`))
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("超大 body 应为 413，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if got := decodeErr(t, rec)["code"]; got != "payload_too_large" {
		t.Fatalf("错误码 = %q, want payload_too_large", got)
	}
}

func TestChainValidatesRequest(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer down.close()
	ch := buildChain(t, cfgWithDownstream(down.addr()), nil)

	cases := []struct {
		name    string
		path    string
		headers map[string]string
		body    io.Reader
	}{
		{
			name:    "缺少必填头",
			path:    "/v1/strict?q=1",
			headers: map[string]string{"X-API-Key": "k1"},
			body:    bytes.NewBufferString(`{"name":"ok"}`),
		},
		{
			name:    "缺少必填查询参数",
			path:    "/v1/strict",
			headers: map[string]string{"X-API-Key": "k1", "X-Trace": "t"},
			body:    bytes.NewBufferString(`{"name":"ok"}`),
		},
		{
			name:    "Content-Type 不支持",
			path:    "/v1/strict?q=1",
			headers: map[string]string{"X-API-Key": "k1", "X-Trace": "t", "Content-Type": "text/plain"},
			body:    bytes.NewBufferString(`{"name":"ok"}`),
		},
		{
			name:    "缺少必填 body 字段",
			path:    "/v1/strict?q=1",
			headers: map[string]string{"X-API-Key": "k1", "X-Trace": "t", "Content-Type": "application/json"},
			body:    bytes.NewBufferString(`{"other":1}`),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(ch, http.MethodPost, tc.path, tc.headers, tc.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("应为 400，实际 %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	if down.hits.Load() != 0 {
		t.Fatal("校验失败不应打到下游")
	}
}

// ---------- 429 ----------

func TestChainRateLimitedIs429(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	defer down.close()

	ch := buildChain(t, cfgWithDownstream(down.addr()), nil)
	h := map[string]string{"X-API-Key": "k1"}

	if rec := do(ch, http.MethodGet, "/v1/orders", h, nil); rec.Code != http.StatusOK {
		t.Fatalf("首请求应放行，实际 %d: %s", rec.Code, rec.Body.String())
	}

	rec := do(ch, http.MethodGet, "/v1/orders", h, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("令牌用尽应为 429，实际 %d", rec.Code)
	}
	if got := decodeErr(t, rec)["code"]; got != "rate_limited" {
		t.Fatalf("错误码 = %q, want rate_limited", got)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("429 必须带 Retry-After")
	}
	if down.hits.Load() != 1 {
		t.Fatalf("被限流的请求不应打到下游，hits = %d", down.hits.Load())
	}
}

// ---------- 重试：每次尝试都对整条链可见 ----------

// TestChainRetriesAndEveryAttemptIsVisible 用真实下游验证重试：
// 503 连续两次后 200，配 max_attempts=3，最终必须 200 且下游恰好被打 3 次。
//
// 这正是"回调点"相对"阶段"的收益：重试循环在链里而不是在某个模块内部，
// 每个尝试都会触发 HookForward，别的模块因此能按 attempt 做判断。
func TestChainRetriesAndEveryAttemptIsVisible(t *testing.T) {
	// 计数器定义在闭包外面：闭包里不能引用由它自己初始化出来的变量。
	var attempts atomic.Int32
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("recovered"))
	})
	defer down.close()

	cfg := fmt.Sprintf(downstreamConfig, down.addr())
	ch := buildChain(t, cfg, nil)

	rec := do(ch, http.MethodGet, "/v1/orders", map[string]string{"X-API-Key": "k1"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("重试后应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); got != "recovered" {
		t.Fatalf("body = %q, want recovered（应是最终那次的响应）", got)
	}
	if hits := down.hits.Load(); hits != 3 {
		t.Fatalf("下游应被打 3 次，实际 %d", hits)
	}
}

// TestChainDoesNotRetryNonIdempotent POST 在前两次都失败时只尝试一次。
func TestChainDoesNotRetryNonIdempotent(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	defer down.close()

	cfg := fmt.Sprintf(downstreamConfig, down.addr())
	ch := buildChain(t, cfg, nil)

	rec := do(ch, http.MethodPost, "/v1/orders", map[string]string{"X-API-Key": "k1"},
		bytes.NewBufferString(`{"x":1}`))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("下游 503 应透传，实际 %d", rec.Code)
	}
	if hits := down.hits.Load(); hits != 1 {
		t.Fatalf("POST 非幂等，只应尝试 1 次，实际 %d", hits)
	}
}

// ---------- traceparent / request id 透传 ----------

func TestChainPropagatesTraceHeaders(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	defer down.close()

	cfg := fmt.Sprintf(downstreamConfig, down.addr())
	ch := buildChain(t, cfg, nil)

	const traceID = "0af7651916cd43dd8448eb211c80319c"
	const spanID = "b7ad6b7169203331"
	rec := do(ch, http.MethodGet, "/v1/open", map[string]string{
		"traceparent":  "00-" + traceID + "-" + spanID + "-01",
		"X-Request-Id": "req-from-edge",
		"X-Tenant":     "t1",
	}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("应放行，实际 %d: %s", rec.Code, rec.Body.String())
	}

	got := down.header()
	if tp := got.Get("traceparent"); !strings.Contains(tp, traceID) {
		t.Fatalf("traceparent 未透传: %q", tp)
	}
	if got.Get("X-Request-Id") != "req-from-edge" {
		t.Fatalf("X-Request-Id 未透传: %q", got.Get("X-Request-Id"))
	}
	if xff := got.Get("X-Forwarded-For"); xff == "" {
		t.Fatal("应记录 X-Forwarded-For")
	}
	// hop-by-hop 头不能带出去。
	if got.Get("Connection") != "" {
		t.Fatal("Connection 头不应透传到下游")
	}
}

// ---------- CORS：一个模块，两个回调点 ----------

const corsConfig = `
version: v1
server:
  listen: "127.0.0.1:18080"
  admin_listen: "127.0.0.1:19090"
  default_tenant: t1
  cors:
    allow_origins: ["https://app.example.com"]
    allow_methods: ["GET", "POST"]
    allow_headers: ["X-API-Key", "Content-Type"]
    max_age: 600s
  discovery:
    type: static
    instances:
      - tenant: t1
        service: svc
        cluster: c1
        addr: %s
tenants:
  - name: t1
    services:
      - name: svc
        protocol: http
        timeout: 500ms
        routes:
          - id: r_get
            path: /v1/orders
            methods: [GET]
            cluster: c1
            auth:
              type: api_key
              keys: ["k1"]
        clusters:
          - name: c1
            lb: round_robin
`

func TestChainCORSPreflightShortCircuits(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("预检请求不应打到下游")
	})
	defer down.close()

	ch := buildChain(t, fmt.Sprintf(corsConfig, down.addr()), nil)
	rec := do(ch, http.MethodOptions, "/v1/orders", map[string]string{
		"Origin":                         "https://app.example.com",
		"Access-Control-Request-Method":  "GET",
		"Access-Control-Request-Headers": "X-API-Key",
	}, nil)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("预检应为 204，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("Allow-Origin = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(got, "GET") {
		t.Fatalf("Allow-Methods = %q", got)
	}
	if got := rec.Header().Get("Access-Control-Max-Age"); got != "600" {
		t.Fatalf("Max-Age = %q, want 600", got)
	}
}

func TestChainCORSHeadersOnSuccessAndRejection(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	defer down.close()

	ch := buildChain(t, fmt.Sprintf(corsConfig, down.addr()), nil)
	origin := map[string]string{"Origin": "https://app.example.com"}

	// 成功路径：响应头由 read_response 回调点补上。
	rec := do(ch, http.MethodGet, "/v1/orders", mergeHeaders(origin, map[string]string{"X-API-Key": "k1"}), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("应成功，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("成功响应缺少 CORS 头: %q", got)
	}

	// 被拒绝的路径同样要带头：浏览器只会把带 CORS 头的响应暴露给页面，
	// 否则前端看到的是一个来源错误，而不是真正的 401。
	rec = do(ch, http.MethodGet, "/v1/orders", origin, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("应 401，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("401 响应缺少 CORS 头: %q", got)
	}

	// 不在白名单的来源一个头都不写。
	rec = do(ch, http.MethodGet, "/v1/orders", map[string]string{"Origin": "https://evil.example.com"}, nil)
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("非白名单来源不应拿到 CORS 头: %q", got)
	}
}

func mergeHeaders(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// ---------- panic → 500，且收尾仍然执行 ----------

// panicInvoker 模拟一个写出故障代码的下游调用器。
type panicInvoker struct{}

func (panicInvoker) Protocol() string { return "http" }

func (panicInvoker) InvokeWithCtx(context.Context, *proxy.InvocationRequest) (*proxy.InvocationResponse, error) {
	panic("invoker exploded")
}

func TestChainPanicBecomes500AndFinishes(t *testing.T) {
	logs := &bytes.Buffer{}
	cfg := fmt.Sprintf(downstreamConfig, "127.0.0.1:1")

	// 用一个会在调用时抛 panic 的 invoker 制造链路 panic：
	// 相比在模块里塞测试专用钩子，这种方式不动生产代码就能覆盖真实故障路径。
	reg := proxy.NewRegistry()
	reg.Register(panicInvoker{})
	ch, err := Build(Options{
		State:    newMemState(t, cfg),
		Logger:   logging.New(logging.Config{Writer: logs, Level: logging.LevelInfo}),
		Metrics:  metrics.NewGateway(metrics.NewRegistry()),
		MaxBody:  1024,
		Invokers: reg,
	})
	if err != nil {
		t.Fatalf("装配: %v", err)
	}

	rec := do(ch, http.MethodGet, "/v1/orders", map[string]string{"X-API-Key": "k1"}, nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic 应转成 500，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if got := decodeErr(t, rec)["code"]; got != "internal_error" {
		t.Fatalf("错误码 = %q, want internal_error", got)
	}

	// 关键断言：模块炸了之后收尾回调仍然执行（访问日志写出来了）。
	if !strings.Contains(logs.String(), "access_log") && !strings.Contains(logs.String(), `"route":"r_get"`) {
		t.Fatalf("panic 之后访问日志未写出: %s", logs.String())
	}
}

// ---------- 拓扑 ----------

func TestTopologyReflectsRegisteredOrder(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {})
	defer down.close()

	ch := buildChain(t, fmt.Sprintf(corsConfig, down.addr()), nil)
	topo := Topology(ch)

	want := map[string][]string{
		"before_route":   {"mw_request_id.request.id", "mw_body_limit.body.limit", "mw_cors.cors.request"},
		"found_route":    {},
		"after_route":    {"mw_auth.auth.authenticate", "mw_validate.validate.request", "mw_ratelimit.ratelimit.allow"},
		"forward":        {"mw_upstream.upstream.invoke"},
		"read_response":  {"mw_transformer.response.normalize", "mw_cors.cors.response"},
		"request_finish": {"mw_access_log.access.log", "mw_metrics.metrics.record", "mw_trace.trace.end"},
	}
	for point, handlers := range want {
		got := topo[point]
		if len(got) != len(handlers) {
			t.Fatalf("%s = %v, want %v", point, got, handlers)
		}
		for i := range handlers {
			if got[i] != handlers[i] {
				t.Fatalf("%s[%d] = %q, want %q（顺序应由 order 决定）", point, i, got[i], handlers[i])
			}
		}
	}

	if Topology(nil) != nil {
		t.Fatal("nil 链的拓扑应为 nil")
	}
}

// ---------- 单元级：纯函数 ----------

func TestHostOnly(t *testing.T) {
	cases := map[string]string{
		"gw.example.com:8080": "gw.example.com",
		"gw.example.com":      "gw.example.com",
		"[::1]:8080":          "::1",
		"127.0.0.1:18080":     "127.0.0.1",
		"":                    "",
	}
	for in, want := range cases {
		if got := hostOnly(in); got != want {
			t.Errorf("hostOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

// ---------- IP 黑白名单 ----------

const ipConfig = `
version: v1
server:
  listen: "127.0.0.1:18080"
  admin_listen: "127.0.0.1:19090"
  default_tenant: t1
  ip_allow: ["10.0.0.0/8"]
  ip_deny: ["10.1.2.3"]
  discovery:
    type: static
    instances:
      - tenant: t1
        service: svc
        cluster: c1
        addr: %s
tenants:
  - name: t1
    services:
      - name: svc
        protocol: http
        timeout: 500ms
        routes:
          - id: r_open
            path: /v1/open
            methods: [GET]
            cluster: c1
        clusters:
          - name: c1
            lb: round_robin
`

func TestChainIPAccessControl(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})
	defer down.close()

	ch := buildChain(t, fmt.Sprintf(ipConfig, down.addr()), nil)

	// 白名单内、不在黑名单 → 放行。
	rec := do(ch, http.MethodGet, "/v1/open", map[string]string{"X-Forwarded-For": "10.0.0.9"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("白名单内应放行，实际 %d: %s", rec.Code, rec.Body.String())
	}

	// 白名单内的单点黑名单优先于白名单。
	rec = do(ch, http.MethodGet, "/v1/open", map[string]string{"X-Forwarded-For": "10.1.2.3"}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("黑名单应优先拒绝，实际 %d", rec.Code)
	}

	// 白名单外 → 拒绝。
	rec = do(ch, http.MethodGet, "/v1/open", map[string]string{"X-Forwarded-For": "203.0.113.7"}, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("白名单外应拒绝，实际 %d", rec.Code)
	}
	if down.hits.Load() != 1 {
		t.Fatalf("被 IP 拒绝的请求不应打到下游，hits = %d", down.hits.Load())
	}
}

// ---------- 熔断 ----------

const breakerConfig = `
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
        addr: %s
tenants:
  - name: t1
    services:
      - name: svc
        protocol: http
        timeout: 300ms
        retry:
          max_attempts: 1
        breaker:
          error_rate: 0.5
          min_requests: 2
          open_timeout: 30s
          half_open_requests: 1
        routes:
          - id: r_get
            path: /v1/orders
            methods: [GET]
            cluster: c1
        clusters:
          - name: c1
            lb: round_robin
`

// TestChainCircuitOpensAndFastFails 连续失败把熔断器打开后，
// 后续请求不再打到下游，直接 503 + circuit_open。
func TestChainCircuitOpensAndFastFails(t *testing.T) {
	down := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer down.close()

	ch := buildChain(t, fmt.Sprintf(breakerConfig, down.addr()), nil)
	h := map[string]string{"X-API-Key": "k1"}

	// 前 2 次（min_requests）会真的打下去，失败率 100% > 0.5 → 打开。
	for i := 0; i < 2; i++ {
		if rec := do(ch, http.MethodGet, "/v1/orders", h, nil); rec.Code != http.StatusInternalServerError {
			t.Fatalf("第 %d 次应透传下游 500，实际 %d", i+1, rec.Code)
		}
	}

	before := down.hits.Load()
	rec := do(ch, http.MethodGet, "/v1/orders", h, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("熔断打开应 503，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if got := decodeErr(t, rec)["code"]; got != "circuit_open" {
		t.Fatalf("错误码 = %q, want circuit_open", got)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Fatal("熔断响应应带 Retry-After")
	}
	if down.hits.Load() != before {
		t.Fatalf("熔断期间不应再打下游，hits %d → %d", before, down.hits.Load())
	}
}

// ---------- 调用期查找：实例来自注册中心 ----------

// withSource 用给定实例来源装配治理链，用于验证"每次尝试都重新查实例"。
func withSource(t *testing.T, yaml string, src discovery.DiscoveryClient, mutate func(*Options)) *Chain {
	t.Helper()

	base := newMemState(t, yaml)
	base.source = src

	logs := &bytes.Buffer{}
	opts := Options{
		State:    base,
		Source:   src,
		Logger:   logging.New(logging.Config{Writer: logs, Level: logging.LevelError}),
		Metrics:  metrics.NewGateway(metrics.NewRegistry()),
		MaxBody:  1024,
		Invokers: proxy.NewRegistry(),
		CORS:     base.Snapshot().Config().Server.CORS,
	}
	if mutate != nil {
		mutate(&opts)
	}
	ch, err := Build(opts)
	if err != nil {
		t.Fatalf("装配治理链: %v", err)
	}
	return ch
}

// rotatingSource 第一次给出一个连不通的实例，之后给出活着的实例。
type rotatingSource struct {
	live  string
	calls atomic.Int32
}

func (s *rotatingSource) DiscoverInstances(context.Context, string, string, string) ([]*core.Instance, error) {
	if s.calls.Add(1) == 1 {
		return []*core.Instance{newFakeInstance("127.0.0.1:1")}, nil
	}
	return []*core.Instance{newFakeInstance(s.live)}, nil
}

// erroringSource 模拟注册中心整体不可用：不是"没有实例"，
// 而是连实例列表都拿不到。
type erroringSource struct{ err error }

func (s *erroringSource) DiscoverInstances(context.Context, string, string, string) ([]*core.Instance, error) {
	return nil, s.err
}

// apiKeySubject 复算 api_key 认证后的调用方身份。
//
// 与 policy.apiKeyAuth 的约定一致（"api_key:" + sha256(key) 前 4 字节 hex）：
// 测试要按 users 配灰度规则，就必须知道认证后身份长什么样。
func apiKeySubject(key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("api_key:%x", sum[:4])
}

// listSource 固定返回同一组实例，用于验证负载均衡的分布。
type listSource struct{ insts []*core.Instance }

func (s *listSource) DiscoverInstances(context.Context, string, string, string) ([]*core.Instance, error) {
	return s.insts, nil
}

func newFakeInstance(addr string) *core.Instance {
	return &core.Instance{ID: addr, Address: addr, Weight: 1}
}

// balanceConfig 不带限流：要多打几十个请求来观察负载均衡的分布，
// 被限流计数器干扰就什么都看不出来了。
const balanceConfig = `
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
      - tenant: t1
        service: svc
        cluster: c1
        addr: 127.0.0.1:1
tenants:
  - name: t1
    services:
      - name: svc
        protocol: http
        timeout: 500ms
        routes:
          - id: r_get
            path: /v1/orders
            methods: [GET]
            cluster: c1
            auth:
              type: api_key
              keys: ["k1"]
        clusters:
          - name: c1
            lb: round_robin
`

// ---------- 路由规则（DSL）在真实链路里生效 ----------

const canaryConfig = `
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
        cluster: base
        addr: %s
      - tenant: t1
        service: svc
        cluster: canary
        addr: %s
tenants:
  - name: t1
    services:
      - name: svc
        protocol: http
        timeout: 500ms
        routes:
          # 灰度：带 X-Canary 的请求走 canary 集群。
          # 规则在启动时编译，请求期只做求值。
          - id: r_canary
            rule: '$.header["X-Canary"] == "true"'
            priority: 10
            cluster: canary
            auth:
              type: api_key
              keys: ["k1"]
          # 其余请求走 base。
          - id: r_base
            path: /v1/orders
            methods: [GET]
            cluster: base
            auth:
              type: api_key
              keys: ["k1"]
        clusters:
          - name: base
          - name: canary
`

// TestChainRuleRoutesToCanary 验证 DSL 规则在完整链路里生效：
// 命中规则的请求走 canary，未命中的落到静态路由的 base 集群。
func TestChainRuleRoutesToCanary(t *testing.T) {
	base := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("base"))
	})
	defer base.close()
	canary := newTestDownstream(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("canary"))
	})
	defer canary.close()

	cfg := fmt.Sprintf(canaryConfig, base.addr(), canary.addr())
	ch := buildChain(t, cfg, nil)

	// 带灰度头的请求必须稳定落到 canary。
	for i := 0; i < 5; i++ {
		rec := do(ch, http.MethodGet, "/v1/orders", map[string]string{
			"X-API-Key": "k1",
			"X-Canary":  "true",
		}, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("应成功，实际 %d: %s", rec.Code, rec.Body.String())
		}
		if got := rec.Body.String(); got != "canary" {
			t.Fatalf("命中规则应进灰度集群，实际 %q", got)
		}
	}
	if canary.hits.Load() != 5 || base.hits.Load() != 0 {
		t.Fatalf("流量应全部落在灰度集群：canary=%d base=%d",
			canary.hits.Load(), base.hits.Load())
	}

	// 不带灰度头：规则不命中，回落到静态路由。
	rec := do(ch, http.MethodGet, "/v1/orders", map[string]string{"X-API-Key": "k1"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("应成功，实际 %d", rec.Code)
	}
	if got := rec.Body.String(); got != "base" {
		t.Fatalf("未命中规则应走 base，实际 %q", got)
	}
}
