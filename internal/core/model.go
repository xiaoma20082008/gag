package core

import (
	"errors"
	"hash/fnv"
	"net/http"
	"net/url"
	"strings"

	"gag/internal/config"
)

var (
	// ErrTenantNotFound 请求指定的租户不存在。
	ErrTenantNotFound = errors.New("core: tenant not found")
	// ErrRouteNotFound 租户内没有路由命中该请求。
	ErrRouteNotFound = errors.New("core: no route matched")
	// ErrClusterNotFound 集群既不在配置里，注册中心也不认识它。
	ErrClusterNotFound = errors.New("core: cluster not found")
	// ErrNoInstances 集群存在但一个实例都没有。
	ErrNoInstances = errors.New("core: no instance available")
	// ErrInstanceSource 实例来源本身查询失败（注册中心不可达、超时）。
	// 与 ErrNoInstances 区分：这是基础设施故障，不是容量问题。
	ErrInstanceSource = errors.New("core: instance source failed")
	// ErrRouteEval 路由规则的 DSL 求值失败。
	// 这不是"没命中"：规则本身有问题，按配置故障处理。
	ErrRouteEval = errors.New("core: route rule evaluation failed")
)

// Service 是逻辑服务：协议与默认超时。
type Service struct {
	Name     string
	Protocol string
	Timeout  config.Duration
}

// Cluster 是部署单元：地域、版本与负载均衡策略。
type Cluster struct {
	Name        string
	Region      string
	Version     string
	LB          string
	Metadata    map[string]string
	HealthCheck *config.HealthCheckPolicy
}

// Instance 是最终的执行节点。
type Instance struct {
	ID       string
	Host     string
	Port     int
	Address  string // = Host + ":" + Port
	Weight   int
	Metadata map[string]string
}

// Decision 是路由期的查找结果：请求属于哪条路由、哪个服务。
type Decision struct {
	Tenant     string
	Service    string
	RouteID    string
	Protocol   string
	Route      *config.Route
	ServiceCfg *config.Service
}

// RequestMeta 是查找链所需的请求特征。
type RequestMeta struct {
	Tenant   string
	Method   string
	Host     string
	Path     string
	Header   http.Header
	Query    url.Values
	ClientIP string
	User     string
	Region   string
}

// SelectCluster 返回路由指向的目标集群。
//
// 灰度分流已经上移到路由匹配：命中灰度规则的请求会匹配到另一条路由，
// 那条路由自己写着 canary 集群。所以这里不再按 meta 做二次分流——
// 一条请求在路由期就确定了唯一集群，重试不会换版本。
// 保留这个函数是为了让调用方不必判空 route，也留一个将来接
// region/zone 亲和性的口子。
func SelectCluster(route *config.Route, _ *RequestMeta) string {
	if route == nil {
		return ""
	}
	return route.Cluster
}

func parseCookies(h http.Header) map[string]string {
	out := make(map[string]string, 4)
	for _, line := range h["Cookie"] {
		for _, part := range strings.Split(line, ";") {
			part = strings.TrimSpace(part)
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	if want == "" {
		return false
	}
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func hash32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// WeightedCluster 便于控制面展示当前权重分布。
type WeightedCluster struct {
	Cluster string
	Weight  int
	Percent float64
}
