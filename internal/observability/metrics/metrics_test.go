package metrics

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// render 通过 /metrics 端点取文本输出，等价于 Prometheus 的真实抓取路径。
func render(t *testing.T, r *Registry) string {
	t.Helper()
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("抓取端点返回 %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/plain") {
		t.Fatalf("Content-Type = %q, want text/plain", ct)
	}
	return rec.Body.String()
}

// valueOf 直接读取 collector 的当前值，避免为测试在指标层开读取接口。
func valueOf(t *testing.T, m prometheus.Metric) float64 {
	t.Helper()
	var d dto.Metric
	if err := m.Write(&d); err != nil {
		t.Fatalf("读取指标值: %v", err)
	}
	switch {
	case d.Counter != nil:
		return d.Counter.GetValue()
	case d.Gauge != nil:
		return d.Gauge.GetValue()
	default:
		t.Fatalf("不支持的指标类型: %s", d.String())
		return 0
	}
}

func TestCounterAndGauge(t *testing.T) {
	r := NewRegistry()
	g := NewGateway(r)

	c := g.Requests.WithLabelValues("/a", "GET", "200", "t")
	c.Inc()
	c.Add(2)
	if got := valueOf(t, c); got != 3 {
		t.Fatalf("counter = %v, want 3", got)
	}

	in := g.InFlight.WithLabelValues("pre")
	in.Inc()
	in.Inc()
	in.Dec()
	if got := valueOf(t, in); got != 1 {
		t.Fatalf("gauge = %v, want 1", got)
	}
}

func TestGatewayMetricsExposed(t *testing.T) {
	r := NewRegistry()
	g := NewGateway(r)
	g.Requests.WithLabelValues("/a", "GET", "200", "t").Inc()
	g.Duration.WithLabelValues("/a", "200").Observe(0.03)

	out := render(t, r)
	for _, want := range []string{
		"# HELP gateway_requests_total",
		"# TYPE gateway_requests_total counter",
		`gateway_requests_total{method="GET",route="/a",status="200",tenant="t"} 1`,
		"# TYPE gateway_request_duration_seconds histogram",
		`gateway_request_duration_seconds_bucket{route="/a",status="200",le="0.05"} 1`,
		`gateway_request_duration_seconds_count{route="/a",status="200"} 1`,
		// Go runtime 指标由官方 collector 提供，说明 registry 已正确挂载。
		"go_goroutines",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出缺少 %q\n---\n%s", want, out)
		}
	}
}

func TestLabelEscaping(t *testing.T) {
	r := NewRegistry()
	g := NewGateway(r)
	g.Requests.WithLabelValues(`a"b`, "GET", "200", "t").Inc()

	out := render(t, r)
	if !strings.Contains(out, `route="a\"b"`) {
		t.Fatalf("标签转义错误:\n%s", out)
	}
}

func TestHandlerIsReusable(t *testing.T) {
	r := NewRegistry()
	g := NewGateway(r)
	g.Requests.WithLabelValues("/a", "GET", "200", "t").Inc()

	// 同一个 Registry 可以被多次抓取，不能因为 handler 复用时序出错。
	for i := 0; i < 3; i++ {
		if out := render(t, r); !strings.Contains(out, "gateway_requests_total") {
			t.Fatalf("第 %d 次抓取缺少指标", i+1)
		}
	}
	if h := r.Handler(); h == nil {
		t.Fatal("Handler() 不应返回 nil")
	}
}
