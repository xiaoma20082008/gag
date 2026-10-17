package config

import (
	"strings"
	"testing"
)

func TestLoadExampleConfig(t *testing.T) {
	cfg, err := LoadFile("../../configs/gateway.yaml")
	if err != nil {
		t.Fatalf("加载示例配置失败: %v", err)
	}
	if got := len(cfg.Tenants); got != 2 {
		t.Fatalf("租户数量 = %d, want 2", got)
	}
	if cfg.Server.Listen != ":8080" {
		t.Fatalf("listen = %q", cfg.Server.Listen)
	}
	// 默认值应被补齐
	svc := cfg.Tenants[0].Services[0]
	if svc.Protocol != "http" {
		t.Fatalf("protocol 默认值未生效: %q", svc.Protocol)
	}
	if svc.Clusters[0].LB != "round_robin" {
		t.Fatalf("lb 默认值未生效: %q", svc.Clusters[0].LB)
	}
	if svc.Clusters[0].Instances[0].Weight != 1 {
		t.Fatalf("instance 权重默认值未生效: %d", svc.Clusters[0].Instances[0].Weight)
	}
}

func TestSnapshotIsStable(t *testing.T) {
	cfg, err := LoadFile("../../configs/gateway.yaml")
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSnapshot(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash() != b.Hash() {
		t.Fatalf("同一配置两次哈希不一致: %s vs %s", a.Hash(), b.Hash())
	}
	if a.Version() != "v1" {
		t.Fatalf("version = %q, want v1", a.Version())
	}
	if a.DefaultTenant() != "order" {
		t.Fatalf("default tenant = %q", a.DefaultTenant())
	}
}

func TestValidateRejectsBrokenReferences(t *testing.T) {
	cases := []struct {
		name string
		yaml string
		want string
	}{
		{
			name: "route 引用不存在的 cluster",
			yaml: `
version: v1
tenants:
  - name: t
    services:
      - name: s
        routes:
          - id: r
            path: /x
            cluster: missing
        clusters:
          - name: c
            instances:
              - addr: 127.0.0.1:1
`,
			want: "不存在的 cluster",
		},
		{
			name: "route id 重复",
			yaml: `
version: v1
tenants:
  - name: t
    services:
      - name: s
        routes:
          - id: r
            path: /x
            cluster: c
          - id: r
            path: /y
            cluster: c
        clusters:
          - name: c
            instances:
              - addr: 127.0.0.1:1
`,
			want: "重复",
		},
		{
			name: "api_key 缺少 keys",
			yaml: `
version: v1
tenants:
  - name: t
    services:
      - name: s
        auth:
          type: api_key
        routes:
          - id: r
            path: /x
            cluster: c
        clusters:
          - name: c
            instances:
              - addr: 127.0.0.1:1
`,
			want: "未配置 keys",
		},
		{
			name: "非法实例地址",
			yaml: `
version: v1
tenants:
  - name: t
    services:
      - name: s
        routes:
          - id: r
            path: /x
            cluster: c
        clusters:
          - name: c
            instances:
              - addr: 127.0.0.1
`,
			want: "host:port",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(strings.NewReader(tc.yaml))
			if err == nil {
				t.Fatal("期望校验失败，实际通过")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误信息 %q 不包含 %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	_, err := Load(strings.NewReader(`
version: v1
server:
  listen: ":0"
  unknown_field: true
tenants: []
`))
	if err == nil {
		t.Fatal("期望未知字段报错")
	}
	if !strings.Contains(err.Error(), "unknown_field") {
		t.Fatalf("错误信息未指出未知字段: %v", err)
	}
}

func TestAdminListenMustDifferFromListen(t *testing.T) {
	base := `
version: v1
server:
  listen: "%s"
  admin_listen: "%s"
tenants:
  - name: t1
    services:
      - name: svc
        routes:
          - id: r1
            path: /x
            cluster: c1
        clusters:
          - name: c1
            instances:
              - addr: 127.0.0.1:1
`
	cases := []struct {
		name      string
		listen    string
		admin     string
		wantError bool
	}{
		{"分离端口", ":8080", "127.0.0.1:9090", false},
		{"完全同址", "127.0.0.1:8080", "127.0.0.1:8080", true},
		{"通配与具体地址抢同一端口", ":8080", "127.0.0.1:8080", true},
		{"不同主机同端口", "127.0.0.1:8080", "127.0.0.2:8080", false},
		{"非法地址", ":8080", "127.0.0.1", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			yaml := strings.Replace(strings.Replace(base, "%s", c.listen, 1), "%s", c.admin, 1)
			_, err := Load(strings.NewReader(yaml))
			if c.wantError && err == nil {
				t.Fatalf("期望校验失败，实际通过")
			}
			if !c.wantError && err != nil {
				t.Fatalf("期望通过，实际失败: %v", err)
			}
		})
	}
}
