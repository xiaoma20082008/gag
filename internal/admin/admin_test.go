package admin

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"gag/internal/config"
	"gag/internal/controlplane"
	"gag/internal/observability/metrics"
)

const adminTestConfig = `
version: v1
server:
  listen: "127.0.0.1:18080"
  admin_listen: "127.0.0.1:19090"
tenants:
  - name: t1
    services:
      - name: svc
        protocol: http
        routes:
          - id: r1
            path: /v1/orders
            cluster: c1
        clusters:
          - name: c1
            instances:
              - addr: 127.0.0.1:1
`

func newTestAdmin(t *testing.T, deps Deps) *AdminServer {
	t.Helper()
	gin.SetMode(gin.TestMode)
	s := NewAdminWith(&AdminConfig{Host: "127.0.0.1", Port: 19090}, deps)
	if err := s.Initialize(); err != nil {
		t.Fatalf("初始化: %v", err)
	}
	return s
}

func newStore(t *testing.T) *controlplane.Store {
	t.Helper()
	cfg, err := config.Load(strings.NewReader(adminTestConfig))
	if err != nil {
		t.Fatalf("加载配置: %v", err)
	}
	store := controlplane.NewStore()
	if _, err := store.Publish(cfg, "test", "bootstrap"); err != nil {
		t.Fatalf("发布初始配置: %v", err)
	}
	return store
}

func call(s *AdminServer, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func TestAdminProbesAndMetrics(t *testing.T) {
	registry := metrics.NewRegistry()
	gw := metrics.NewGateway(registry)
	// prometheus 的 *Vec 在没有子指标时不会出现在 exposition 里，
	// 因此先制造一条样本，才能验证 /metrics 确实抓到了网关指标。
	gw.Requests.WithLabelValues("/a", "GET", "200", "t").Inc()
	s := newTestAdmin(t, Deps{Metrics: registry})

	if got := call(s, http.MethodGet, "/healthz", "", nil); got.Code != http.StatusOK {
		t.Fatalf("healthz 应为 200，实际 %d", got.Code)
	}
	if got := call(s, http.MethodGet, "/readyz", "", nil); got.Code != http.StatusOK {
		t.Fatalf("readyz 应为 200，实际 %d", got.Code)
	}
	got := call(s, http.MethodGet, "/metrics", "", nil)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "gateway_requests_total") {
		t.Fatalf("metrics 应输出网关指标，实际 %d: %s", got.Code, got.Body.String())
	}
}

func TestAdminReadinessReportsFailure(t *testing.T) {
	s := newTestAdmin(t, Deps{Ready: func() error { return errors.New("配置未装载") }})
	got := call(s, http.MethodGet, "/readyz", "", nil)
	if got.Code != http.StatusServiceUnavailable {
		t.Fatalf("未就绪应为 503，实际 %d", got.Code)
	}
	if !strings.Contains(got.Body.String(), "配置未装载") {
		t.Fatalf("未就绪响应应包含原因: %s", got.Body.String())
	}
}

func TestAdminAuth(t *testing.T) {
	s := newTestAdmin(t, Deps{Token: "secret", Store: newStore(t)})

	if got := call(s, http.MethodGet, "/admin/status", "", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("缺少凭证应为 401，实际 %d", got.Code)
	}
	if got := call(s, http.MethodGet, "/admin/status", "", map[string]string{"Authorization": "Bearer wrong"}); got.Code != http.StatusUnauthorized {
		t.Fatalf("错误凭证应为 401，实际 %d", got.Code)
	}
	got := call(s, http.MethodGet, "/admin/status", "", map[string]string{"Authorization": "Bearer secret"})
	if got.Code != http.StatusOK {
		t.Fatalf("正确凭证应为 200，实际 %d: %s", got.Code, got.Body.String())
	}
	if !strings.Contains(got.Body.String(), `"revision":1`) {
		t.Fatalf("status 应包含当前修订号: %s", got.Body.String())
	}

	// 探针不鉴权，否则 K8s 探活会被 401 打断。
	if got := call(s, http.MethodGet, "/healthz", "", nil); got.Code != http.StatusOK {
		t.Fatalf("探针不应鉴权，实际 %d", got.Code)
	}
}

func TestAdminWithoutTokenOnlyAllowsLoopback(t *testing.T) {
	s := newTestAdmin(t, Deps{Store: newStore(t)})

	if got := call(s, http.MethodGet, "/admin/status", "", nil); got.Code != http.StatusUnauthorized {
		t.Fatalf("非回环来源应被拒绝，实际 %d", got.Code)
	}

	req := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("回环来源应放行，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAdminPublishAndRollback(t *testing.T) {
	store := newStore(t)
	s := newTestAdmin(t, Deps{Token: "secret", Store: store})
	auth := map[string]string{"Authorization": "Bearer secret", "X-Admin-User": "alice"}

	// 校验失败必须在入库前被挡住，否则数据面会整体拒绝热加载。
	bad := call(s, http.MethodPost, "/admin/config", "tenants: []\n", auth)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("非法配置应为 400，实际 %d: %s", bad.Code, bad.Body.String())
	}
	if store.Current().ID != 1 {
		t.Fatal("非法配置不应产生新修订")
	}

	next := strings.Replace(adminTestConfig, "version: v1", "version: v2", 1)
	ok := call(s, http.MethodPost, "/admin/config?comment=canary", next, auth)
	if ok.Code != http.StatusOK {
		t.Fatalf("发布应为 200，实际 %d: %s", ok.Code, ok.Body.String())
	}
	if store.Current().ID != 2 || store.Current().Author != "alice" {
		t.Fatalf("发布未生效: %+v", store.Current())
	}

	raw := call(s, http.MethodGet, "/admin/config", "", auth)
	if raw.Code != http.StatusOK || !strings.Contains(raw.Body.String(), "version: v2") {
		t.Fatalf("读取当前配置失败: %d %s", raw.Code, raw.Body.String())
	}

	rb := call(s, http.MethodPost, "/admin/rollback?id=1", "", auth)
	if rb.Code != http.StatusOK {
		t.Fatalf("回滚应为 200，实际 %d: %s", rb.Code, rb.Body.String())
	}
	// 回滚是"以旧内容生成新修订"，审计链路不被截断。
	cur := store.Current()
	if cur.ID != 3 || cur.Version != "v1" {
		t.Fatalf("回滚后应生效 v1 的内容并生成新修订，实际 #%d %s", cur.ID, cur.Version)
	}
	if !strings.Contains(cur.Comment, "rollback to #1") {
		t.Fatalf("回滚记录应保留来源: %s", cur.Comment)
	}

	if got := call(s, http.MethodPost, "/admin/rollback?id=999", "", auth); got.Code != http.StatusNotFound {
		t.Fatalf("不存在的修订应返回 404，实际 %d", got.Code)
	}
}

func TestAdminControlPlaneNotConfigured(t *testing.T) {
	s := newTestAdmin(t, Deps{Token: "secret"})
	got := call(s, http.MethodGet, "/admin/status", "", map[string]string{"Authorization": "Bearer secret"})
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"control_plane":false`) {
		t.Fatalf("未接入控制面时应如实上报: %d %s", got.Code, got.Body.String())
	}
}
