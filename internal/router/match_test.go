package router

import (
	"net/http"
	"net/url"
	"testing"

	"gag/internal/config"
	"gag/internal/core"
	"gag/internal/script"
)

// matchEnv 是测试用的请求特征。
func matchEnv(method, path string, hdr map[string]string, query map[string]string) *core.RequestMeta {
	h := http.Header{}
	for k, v := range hdr {
		h.Set(k, v)
	}
	q := url.Values{}
	for k, v := range query {
		q.Set(k, v)
	}
	return &core.RequestMeta{Method: method, Path: path, Header: h, Query: q}
}

func mustMatcher(t *testing.T, r *config.Route) *Matcher {
	t.Helper()
	m, err := NewMatcher(r)
	if err != nil {
		t.Fatalf("编译匹配器失败: %v", err)
	}
	return m
}

func mustMatch(t *testing.T, m *Matcher, meta *core.RequestMeta) bool {
	t.Helper()
	ok, err := m.Match(meta, &script.Env{})
	if err != nil {
		t.Fatalf("匹配出错: %v", err)
	}
	return ok
}

// TestExactPath 验证精确路径只命中完全相同的那一条。
func TestExactPath(t *testing.T) {
	m := mustMatcher(t, &config.Route{ID: "r", Path: "/v1/orders"})

	if !mustMatch(t, m, matchEnv("GET", "/v1/orders", nil, nil)) {
		t.Error("/v1/orders 应当命中")
	}
	for _, p := range []string{"/v1/orders/1", "/v1/order", "/v1", "/"} {
		if mustMatch(t, m, matchEnv("GET", p, nil, nil)) {
			t.Errorf("%s 不该命中精确路径 /v1/orders", p)
		}
	}
}

// TestPrefixBoundary 守住 /v1/order/* 不误吃 /v1/orders。
//
// 少了段边界判断，前缀路由会把所有同前缀的兄弟路径一起吞掉，
// 而且看起来"能用"，直到某天两个服务互相抢流量。
func TestPrefixBoundary(t *testing.T) {
	m := mustMatcher(t, &config.Route{ID: "r", Path: "/v1/order/*"})

	if !mustMatch(t, m, matchEnv("GET", "/v1/order/1", nil, nil)) {
		t.Error("/v1/order/1 应当命中")
	}
	// 前缀本身也算命中：/v1/order/* 通常希望 /v1/order 也能进。
	if !mustMatch(t, m, matchEnv("GET", "/v1/order", nil, nil)) {
		t.Error("/v1/order 应当命中（前缀自身）")
	}
	for _, p := range []string{"/v1/orders", "/v1/orders/1", "/v1/orderx"} {
		if mustMatch(t, m, matchEnv("GET", p, nil, nil)) {
			t.Errorf("%s 不该命中 /v1/order/*", p)
		}
	}
}

// TestMethods 验证方法过滤，大小写不敏感。
func TestMethods(t *testing.T) {
	m := mustMatcher(t, &config.Route{ID: "r", Path: "/v1/orders", Methods: []string{"get", "POST"}})

	for _, method := range []string{"GET", "get", "POST", "post"} {
		if !mustMatch(t, m, matchEnv(method, "/v1/orders", nil, nil)) {
			t.Errorf("%s 应当命中", method)
		}
	}
	for _, method := range []string{"PUT", "DELETE", "HEAD"} {
		if mustMatch(t, m, matchEnv(method, "/v1/orders", nil, nil)) {
			t.Errorf("%s 不该命中", method)
		}
	}
}

// TestHost 验证 host 过滤，大小写不敏感。
func TestHost(t *testing.T) {
	m := mustMatcher(t, &config.Route{ID: "r", Path: "/x", Host: "API.Example.com"})

	// 带 host 条件的路由，遇到没有 Host 的请求不该命中。
	if mustMatch(t, m, matchEnv("GET", "/x", nil, nil)) {
		t.Error("空 host 不该命中带 host 条件的路由")
	}

	meta := matchEnv("GET", "/x", nil, nil)
	meta.Host = "api.example.com"
	if !mustMatch(t, m, meta) {
		t.Error("大小写不同的同一 host 应当命中")
	}

	meta.Host = "other.example.com"
	if mustMatch(t, m, meta) {
		t.Error("不同 host 不该命中")
	}
}

// TestHeadersAndQuery 验证头与查询参数的精确匹配。
func TestHeadersAndQuery(t *testing.T) {
	m := mustMatcher(t, &config.Route{
		ID:      "r",
		Path:    "/x",
		Headers: map[string]string{"X-Env": "canary"},
		Query:   map[string]string{"debug": "1"},
	})

	both := matchEnv("GET", "/x", map[string]string{"X-Env": "canary"}, map[string]string{"debug": "1"})
	if !mustMatch(t, m, both) {
		t.Error("头与查询都满足时应当命中")
	}
	// 缺任一个都不命中——头/查询是"与"关系。
	if mustMatch(t, m, matchEnv("GET", "/x", map[string]string{"X-Env": "canary"}, nil)) {
		t.Error("缺查询参数不该命中")
	}
	if mustMatch(t, m, matchEnv("GET", "/x", nil, map[string]string{"debug": "1"})) {
		t.Error("缺请求头不该命中")
	}
}

// TestScoreOrdering 验证结构特异性排序：精确路径 > 前缀路径 > 仅规则。
//
// 这个顺序决定"同一请求命中多条路由时谁先"，是灰度能否生效的前提；
// 仅规则的路由必须排在最后，否则它会抢走本该走静态路由的请求。
func TestScoreOrdering(t *testing.T) {
	exact := mustMatcher(t, &config.Route{ID: "exact", Path: "/v1/orders"})
	prefix := mustMatcher(t, &config.Route{ID: "prefix", Path: "/v1/orders/*"})
	ruleOnly := mustMatcher(t, &config.Route{ID: "rule", Rule: `http.method == "GET"`})

	if !(exact.Score() > prefix.Score()) {
		t.Errorf("精确路径分数应高于前缀: exact=%d prefix=%d", exact.Score(), prefix.Score())
	}
	if !(prefix.Score() > ruleOnly.Score()) {
		t.Errorf("前缀路径分数应高于仅规则: prefix=%d rule=%d", prefix.Score(), ruleOnly.Score())
	}
	if ruleOnly.Score() != 0 {
		t.Errorf("仅规则的路由不应有结构分，实际 %d", ruleOnly.Score())
	}
}

// TestLongerPrefixScoresHigher 验证同类型里更长（更具体）的前缀优先。
func TestLongerPrefixScoresHigher(t *testing.T) {
	short := mustMatcher(t, &config.Route{ID: "short", Path: "/v1/*"})
	long := mustMatcher(t, &config.Route{ID: "long", Path: "/v1/orders/*"})

	if long.Score() <= short.Score() {
		t.Errorf("更长前缀应更具体: long=%d short=%d", long.Score(), short.Score())
	}
}
