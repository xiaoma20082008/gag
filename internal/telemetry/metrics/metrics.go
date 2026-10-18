// Package metrics 基于 prometheus 官方客户端提供网关指标。
//
// 只保留一层很薄的 Registry 包装，目的是让 /metrics 端点与指标注册共享同一个
// 实例：指标定义直接用 prometheus 原生的 CounterVec / HistogramVec，
// 文本 exposition、分位数、抓取协商都交给官方实现，避免自维护协议细节。
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Namespace 是所有网关自有指标的前缀。
const Namespace = "gateway"

// DefaultBuckets 是延迟类指标的默认分桶（秒）。
//
// 覆盖 1ms ~ 10s：网关自身耗时集中在毫秒级，下游调用可能到秒级，
// 单一分桶范围无法同时看清两段，因此保留较宽区间。
var DefaultBuckets = []float64{0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Registry 是网关的指标注册表，同时实现 http.Handler 作为抓取端点。
type Registry struct {
	*prometheus.Registry

	// handler 在构造时建好：每次抓取都重新构造会把 HandlerOpts 的校验
	// 与 writer 包装重复做一遍，对高频抓取没有意义。
	handler http.Handler
}

// NewRegistry 创建注册表，并挂上 Go runtime 与进程指标。
//
// 网关的 GC 停顿、goroutine 数量、常驻内存往往比业务指标更早暴露问题，
// 这些指标由官方 collectors 提供，不需要自己维护。
func NewRegistry() *Registry {
	r := prometheus.NewRegistry()
	r.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	return &Registry{
		Registry: r,
		handler:  promhttp.HandlerFor(r, promhttp.HandlerOpts{}),
	}
}

// Handler 返回 Prometheus 抓取端点。
func (r *Registry) Handler() http.Handler { return r.handler }

// ServeHTTP 让 Registry 可以直接作为 http.Handler 注入管理端口。
func (r *Registry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.handler.ServeHTTP(w, req)
}
