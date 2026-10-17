package routing

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"gag/internal/config"
)

const testConfig = `
version: v1
server:
  listen: ":0"
  default_tenant: t1
tenants:
  - name: t1
    services:
      - name: svc
        timeout: 1s
        routes:
          - id: exact
            path: /a
            methods: [GET]
            cluster: c1
          - id: prefix
            path: /a/*
            methods: [GET]
            cluster: c1
          - id: split
            path: /split
            cluster: c1
            split:
              - cluster: c2
                match:
                  headers:
                    X-Canary: "true"
              - cluster: c2
                weight: 5
              - cluster: c1
                weight: 95
          - id: rewritten
            path: /v1/items/*
            methods: [GET]
            cluster: c1
            rewrite:
              strip_prefix: /v1
        clusters:
          - name: c1
            lb: round_robin
            instances:
              - addr: 127.0.0.1:1
              - addr: 127.0.0.1:2
          - name: c2
            lb: round_robin
            instances:
              - addr: 127.0.0.1:3
`

func buildTable(t *testing.T) (*Table, *config.Snapshot) {
	t.Helper()
	cfg, err := config.Load(strings.NewReader(testConfig))
	if err != nil {
		t.Fatalf("加载配置: %v", err)
	}
	snap, err := config.NewSnapshot(cfg)
	if err != nil {
		t.Fatalf("构建快照: %v", err)
	}
	table, err := NewTable(snap, NewHealth())
	if err != nil {
		t.Fatalf("构建路由表: %v", err)
	}
	return table, snap
}

func meta(method, path string) *RequestMeta {
	return &RequestMeta{
		Method: method,
		Path:   path,
		Header: http.Header{},
		Query:  map[string][]string{},
	}
}

func TestResolvePriority(t *testing.T) {
	table, _ := buildTable(t)

	res, err := table.Resolve(meta("GET", "/a"))
	if err != nil {
		t.Fatalf("精确匹配失败: %v", err)
	}
	if res.RouteID != "exact" {
		t.Fatalf("期望命中 exact，实际 %s", res.RouteID)
	}

	res, err = table.Resolve(meta("GET", "/a/b"))
	if err != nil {
		t.Fatalf("前缀匹配失败: %v", err)
	}
	if res.RouteID != "prefix" {
		t.Fatalf("期望命中 prefix，实际 %s", res.RouteID)
	}
}

func TestResolveMethodAndTenant(t *testing.T) {
	table, _ := buildTable(t)

	if _, err := table.Resolve(meta("POST", "/a")); !errors.Is(err, ErrRouteNotFound) {
		t.Fatalf("POST /a 应无匹配，实际: %v", err)
	}

	m := meta("GET", "/a")
	m.Tenant = "nope"
	if _, err := table.Resolve(m); !errors.Is(err, ErrTenantNotFound) {
		t.Fatalf("未知租户应报错，实际: %v", err)
	}
}

func TestResolveSplit(t *testing.T) {
	table, _ := buildTable(t)

	m := meta("GET", "/split")
	m.Header.Set("X-Canary", "true")
	res, err := table.Resolve(m)
	if err != nil {
		t.Fatal(err)
	}
	if res.Cluster != "c2" {
		t.Fatalf("带灰度头应进入 c2，实际 %s", res.Cluster)
	}

	// 权重分流：5% 到 c2，使用不同客户端 IP 验证比例。
	c2 := 0
	total := 2000
	for i := 0; i < total; i++ {
		m := meta("GET", "/split")
		m.ClientIP = fmt.Sprintf("10.0.%d.%d", i/250, i%250)
		res, err := table.Resolve(m)
		if err != nil {
			t.Fatal(err)
		}
		if res.Cluster == "c2" {
			c2++
		}
	}
	ratio := float64(c2) / float64(total) * 100
	if ratio < 2 || ratio > 9 {
		t.Fatalf("灰度比例 %.2f%% 偏离预期 5%%", ratio)
	}
}

func TestResolveNoHealthyInstance(t *testing.T) {
	table, _ := buildTable(t)
	health := table.health
	health.MarkDown(Key("t1", "svc", "c1", "127.0.0.1:1"))
	health.MarkDown(Key("t1", "svc", "c1", "127.0.0.1:2"))

	if _, err := table.Resolve(meta("GET", "/a")); !errors.Is(err, ErrNoHealthyInstance) {
		t.Fatalf("全部实例下线应报错，实际: %v", err)
	}
	health.MarkUp(Key("t1", "svc", "c1", "127.0.0.1:1"))
	if _, err := table.Resolve(meta("GET", "/a")); err != nil {
		t.Fatalf("实例恢复后应可路由: %v", err)
	}
}

func TestRewritePath(t *testing.T) {
	table, _ := buildTable(t)
	res, err := table.Resolve(meta("GET", "/v1/items/42"))
	if err != nil {
		t.Fatal(err)
	}
	got := RewritePath("/v1/items/42", res.Rewrite)
	if got != "/items/42" {
		t.Fatalf("rewrite = %q, want /items/42", got)
	}
}
