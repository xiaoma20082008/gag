package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// 同一份配置的 YAML 与 JSON 写法：用来验证两种 loader 走的是同一条流水线
// （默认值、校验、快照哈希都必须一致）。
const loaderTestYAML = `
version: v1
tenants:
  - name: order
    services:
      - name: order-service
        timeout: 2s
        routes:
          - path: /v1/orders
            cluster: hz
        clusters:
          - name: hz
            lb: least_conn
`

const loaderTestJSON = `{
  "version": "v1",
  "tenants": [
    {
      "name": "order",
      "services": [
        {
          "name": "order-service",
          "timeout": "2s",
          "routes": [{"path": "/v1/orders", "cluster": "hz"}],
          "clusters": [{"name": "hz", "lb": "least_conn"}]
        }
      ]
    }
  ]
}`

func TestNewLoaderPicksByExtension(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"configs/gateway.yaml", "yaml"},
		{"configs/gateway.yml", "yaml"},
		{"configs/GATEWAY.YAML", "yaml"}, // 扩展名大小写不敏感
		{"configs/gateway.json", "json"},
	}
	for _, c := range cases {
		loader, err := NewLoader(c.path)
		if err != nil {
			t.Fatalf("NewLoader(%q) 报错: %v", c.path, err)
		}
		if got := loaderName(loader); got != c.want {
			t.Errorf("NewLoader(%q) = %s, want %s", c.path, got, c.want)
		}
	}
}

// 拿不到扩展名或扩展名不认识时必须报错，而不是猜内容：
// 猜错的后果是把一份写坏的配置解析成"合法的空配置"。
func TestNewLoaderRejectsUnknownExtension(t *testing.T) {
	for _, path := range []string{"configs/gateway", "configs/gateway.toml", "configs/gateway.conf"} {
		if _, err := NewLoader(path); err == nil {
			t.Errorf("NewLoader(%q) 应该报错", path)
		}
	}
}

func TestLoaderFor(t *testing.T) {
	cases := []struct {
		format Format
		want   string
	}{
		{FormatYAML, "yaml"},
		{FormatJSON, "json"},
		{"", "yaml"}, // 未声明格式时按 YAML
		{"JSON", "json"},
	}
	for _, c := range cases {
		loader, err := LoaderFor(c.format)
		if err != nil {
			t.Fatalf("LoaderFor(%q) 报错: %v", c.format, err)
		}
		if got := loaderName(loader); got != c.want {
			t.Errorf("LoaderFor(%q) = %s, want %s", c.format, got, c.want)
		}
	}
	if _, err := LoaderFor("toml"); err == nil {
		t.Fatal("LoaderFor(toml) 应该报错")
	}
}

// JSON 必须和 YAML 一样补齐默认值：这条是本次统一加载的重点，
// 否则同一份配置换个格式就会在数据面表现不同。
func TestJsonLoaderAppliesDefaults(t *testing.T) {
	cfg, err := (&JsonLoader{}).Decode(strings.NewReader(loaderTestJSON))
	if err != nil {
		t.Fatalf("解析 JSON 失败: %v", err)
	}
	if cfg.Server.Listen != DefaultListen || cfg.Server.AdminListen != DefaultAdminListen {
		t.Fatalf("端口默认值未生效: %q / %q", cfg.Server.Listen, cfg.Server.AdminListen)
	}
	svc := cfg.Tenants[0].Services[0]
	if svc.Protocol != "http" {
		t.Fatalf("protocol 默认值未生效: %q", svc.Protocol)
	}
	if svc.Routes[0].ID == "" {
		t.Fatal("route id 未按 service+path 生成")
	}
	if svc.Routes[0].Timeout != Duration(2*time.Second) {
		t.Fatalf("route timeout 未继承 service 默认值: %v", svc.Routes[0].Timeout)
	}
}

// 两种格式解析出的配置必须产生同一个快照哈希——
// 哈希是热加载"策略是否变化"的判据，格式不该影响它。
func TestYamlAndJsonProduceSameSnapshot(t *testing.T) {
	fromYAML, err := (&YamlLoader{}).Decode(strings.NewReader(loaderTestYAML))
	if err != nil {
		t.Fatalf("解析 YAML 失败: %v", err)
	}
	fromJSON, err := (&JsonLoader{}).Decode(strings.NewReader(loaderTestJSON))
	if err != nil {
		t.Fatalf("解析 JSON 失败: %v", err)
	}

	a, err := NewSnapshot(fromYAML)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSnapshot(fromJSON)
	if err != nil {
		t.Fatal(err)
	}
	if a.Hash() != b.Hash() {
		t.Fatalf("YAML 与 JSON 的快照哈希不一致: %s vs %s", a.Hash(), b.Hash())
	}
}

// JSON 也要拒绝未知字段，与 YAML 的 KnownFields(true) 对齐。
func TestJsonLoaderRejectsUnknownField(t *testing.T) {
	_, err := (&JsonLoader{}).Decode(strings.NewReader(`{"tenants": [], "surprise": 1}`))
	if err == nil {
		t.Fatal("未知字段应该报错")
	}
	if !strings.Contains(err.Error(), "surprise") {
		t.Fatalf("错误信息应指出字段名: %v", err)
	}
}

func TestJsonLoaderRejectsEmpty(t *testing.T) {
	for _, in := range []string{"", "   \n"} {
		if _, err := (&JsonLoader{}).Decode(strings.NewReader(in)); err == nil {
			t.Fatalf("%q 应该报「配置为空」", in)
		}
	}
}

// JSON 里的 Duration 写法与 YAML 一致（字符串），不接受裸数字。
func TestJsonDurationIsString(t *testing.T) {
	cfg, err := (&JsonLoader{}).Decode(strings.NewReader(`
{
  "tenants": [{"name": "t", "services": [{"name": "s", "timeout": "250ms",
    "routes": [{"path": "/a", "cluster": "c"}], "clusters": [{"name": "c"}]}]}]
}`))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got := cfg.Tenants[0].Services[0].Timeout.Std(); got != 250*time.Millisecond {
		t.Fatalf("timeout = %v, want 250ms", got)
	}

	if _, err := (&JsonLoader{}).Decode(strings.NewReader(`
{"tenants": [{"name": "t", "services": [{"name": "s", "timeout": 1000}]}]}`)); err == nil {
		t.Fatal("裸数字时长应该报错")
	}
}

// 校验失败要能从 loader 里透出来：坏配置不能进数据面。
func TestLoadersPropagateValidationErrors(t *testing.T) {
	if _, err := (&YamlLoader{}).Decode(strings.NewReader("tenants: []")); err == nil {
		t.Fatal("YAML 空 tenants 应该校验失败")
	}
	if _, err := (&JsonLoader{}).Decode(strings.NewReader(`{"tenants": []}`)); err == nil {
		t.Fatal("JSON 空 tenants 应该校验失败")
	}
}

// LoadFile 按扩展名分派，且错误信息里带文件路径。
func TestLoadFileDispatchesByExtension(t *testing.T) {
	dir := t.TempDir()

	yamlPath := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(yamlPath, []byte(loaderTestYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	jsonPath := filepath.Join(dir, "gateway.json")
	if err := os.WriteFile(jsonPath, []byte(loaderTestJSON), 0o600); err != nil {
		t.Fatal(err)
	}

	fromYAML, err := LoadFile(yamlPath)
	if err != nil {
		t.Fatalf("加载 YAML 文件失败: %v", err)
	}
	fromJSON, err := LoadFile(jsonPath)
	if err != nil {
		t.Fatalf("加载 JSON 文件失败: %v", err)
	}
	if fromYAML.Tenants[0].Name != fromJSON.Tenants[0].Name {
		t.Fatalf("两种格式加载结果不一致: %q vs %q",
			fromYAML.Tenants[0].Name, fromJSON.Tenants[0].Name)
	}

	badPath := filepath.Join(dir, "gateway.toml")
	if err := os.WriteFile(badPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(badPath); err == nil {
		t.Fatal("未知扩展名应该报错")
	}

	missing := filepath.Join(dir, "nope.yaml")
	if _, err := LoadFile(missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("缺失文件的错误应包含路径: %v", err)
	}
}

// ConfigLoader 是外部唯一的文件加载入口，两个实现都必须满足它。
func TestLoadersImplementConfigLoader(t *testing.T) {
	var _ ConfigLoader = (*YamlLoader)(nil)
	var _ ConfigLoader = (*JsonLoader)(nil)
	var _ Decoder = (*YamlLoader)(nil)
	var _ Decoder = (*JsonLoader)(nil)
}

func loaderName(l ConfigLoader) string {
	switch l.(type) {
	case *YamlLoader:
		return "yaml"
	case *JsonLoader:
		return "json"
	default:
		return "unknown"
	}
}
