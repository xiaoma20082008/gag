package config

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// minValidYAML 是一份能通过校验的最小配置，version 用来区分内容。
func minValidYAML(version string) string {
	return `version: ` + version + `
server:
  listen: ":8080"
  admin_listen: "127.0.0.1:9090"
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
            lb: round_robin
`
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "gateway.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("写临时配置失败: %v", err)
	}
	return path
}

func newTestFileClient(t *testing.T, path string, interval time.Duration) *FileConfigClient {
	t.Helper()
	c, err := NewFileConfigClient(&ConfigClientConfig{Type: "file", Path: path, Interval: Duration(interval)})
	if err != nil {
		t.Fatalf("构造 file 客户端失败: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func TestFileClientGet(t *testing.T) {
	content := minValidYAML("v1")
	c := newTestFileClient(t, writeTempConfig(t, content), 10*time.Millisecond)

	if c.Type() != "file" {
		t.Errorf("Type() = %q，期望 file", c.Type())
	}
	if got := c.Get(context.Background(), "", ""); got != content {
		t.Errorf("空 key 应读取默认路径，实际长度 %d", len(got))
	}

	// 不存在的 key：必须返回 def，而不是空串冒充成功。
	if got := c.Get(context.Background(), "/nonexistent/gateway.yaml", "fallback"); got != "fallback" {
		t.Errorf("读不到时应返回 def，实际 %q", got)
	}

	// 已取消的上下文不该再读盘。
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := c.Get(ctx, "", "cancelled"); got != "cancelled" {
		t.Errorf("ctx 已取消时应返回 def，实际 %q", got)
	}
}

// TestFileClientConstructorFailsEarly 验证"路径写错立刻失败"。
//
// 如果拖到第一次 Get 才失败，网关会拿着一份空配置启动，
// 表现成"所有请求 404"，比启动失败难查得多。
func TestFileClientConstructorFailsEarly(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name string
		cfg  *ConfigClientConfig
	}{
		{"配置为 nil", nil},
		{"缺少 path", &ConfigClientConfig{Type: "file"}},
		{"path 只有空白", &ConfigClientConfig{Type: "file", Path: "   "}},
		{"文件不存在", &ConfigClientConfig{Type: "file", Path: filepath.Join(dir, "nope.yaml")}},
		{"扩展名不支持", &ConfigClientConfig{Type: "file", Path: filepath.Join(dir, "gateway.toml")}},
		{"没有扩展名", &ConfigClientConfig{Type: "file", Path: filepath.Join(dir, "gateway")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewFileConfigClient(c.cfg); err == nil {
				t.Fatal("应当报错")
			}
		})
	}
}

// TestFileClientOnChange 验证内容变化才回调。
//
// 关键点：只 touch 不改内容不该触发。ConfigMap 重挂、编辑器保存未改动
// 都会让 mtime 变，那时做一次热加载纯属浪费，还会污染发布记录。
func TestFileClientOnChange(t *testing.T) {
	path := writeTempConfig(t, minValidYAML("v1"))
	c := newTestFileClient(t, path, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	updates := make(chan string, 8)
	c.OnChange(ctx, "", func(newVal string) { updates <- newVal })

	// 内容变化 → 应当回调。
	if err := os.WriteFile(path, []byte(minValidYAML("v2")), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-updates:
		if !strings.Contains(got, "v2") {
			t.Fatalf("回调内容应包含 v2，实际 %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("内容变化后应当回调")
	}

	// 只改 mtime 不改内容 → 不该回调。
	now := time.Now().Add(time.Second)
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-updates:
		t.Fatalf("仅 mtime 变化不该回调，却收到 %q", got)
	case <-time.After(100 * time.Millisecond):
		// 期望：静默
	}
}

// TestFileClientOnChangeStopsOnCancel 验证 ctx 取消后不再回调。
func TestFileClientOnChangeStopsOnCancel(t *testing.T) {
	path := writeTempConfig(t, minValidYAML("v1"))
	c := newTestFileClient(t, path, 10*time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan string, 8)
	c.OnChange(ctx, "", func(newVal string) { updates <- newVal })

	cancel()
	// 给 goroutine 一点时间退出，再改文件。
	time.Sleep(50 * time.Millisecond)

	if err := os.WriteFile(path, []byte(minValidYAML("v2")), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-updates:
		t.Fatalf("ctx 取消后不该再回调，却收到 %q", got)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestFileClientCloseIsIdempotent 验证 Close 可重复调用且能停掉轮询。
func TestFileClientCloseIsIdempotent(t *testing.T) {
	path := writeTempConfig(t, minValidYAML("v1"))
	c, err := NewFileConfigClient(&ConfigClientConfig{Type: "file", Path: path, Interval: Duration(10 * time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}

	updates := make(chan string, 4)
	c.OnChange(context.Background(), "", func(newVal string) { updates <- newVal })

	if err := c.Close(); err != nil {
		t.Fatalf("Close 报错: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("重复 Close 报错: %v", err)
	}

	if err := os.WriteFile(path, []byte(minValidYAML("v2")), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-updates:
		t.Fatalf("Close 后不该再回调，却收到 %q", got)
	case <-time.After(150 * time.Millisecond):
	}
}

// TestSnapshotFromInfersFormat 验证格式推断与回退。
func TestSnapshotFromInfersFormat(t *testing.T) {
	// 扩展名 .json → 按 JSON 解析。
	jsonDoc := `{"version":"v9","server":{"listen":":8080","admin_listen":"127.0.0.1:9090"},
	  "tenants":[{"name":"t1","services":[{"name":"svc","protocol":"http",
	  "routes":[{"id":"r1","path":"/x","cluster":"c1"}],
	  "clusters":[{"name":"c1","lb":"round_robin"}]}]}]}`
	if _, err := SnapshotFrom([]byte(jsonDoc), "gateway.json", ""); err != nil {
		t.Fatalf("按 .json 推断应成功: %v", err)
	}

	// 没有扩展名（例如 nacos 的 dataId）→ 回退 YAML，JSON 是 YAML 子集，也能过。
	if _, err := SnapshotFrom([]byte(jsonDoc), "gateway", ""); err != nil {
		t.Fatalf("无扩展名应回退 YAML 并成功: %v", err)
	}

	// 显式指定的格式优先于扩展名。
	if _, err := SnapshotFrom([]byte(jsonDoc), "gateway.yaml", FormatJSON); err != nil {
		t.Fatalf("显式 FormatJSON 应成功: %v", err)
	}

	// 内容非法 → 报错。
	if _, err := SnapshotFrom([]byte("not: [valid"), "gateway.yaml", ""); err == nil {
		t.Fatal("非法 YAML 应当报错")
	}
}

// TestParseDocumentRejectsUnknownField 守住 strict 解析。
func TestParseDocumentRejectsUnknownField(t *testing.T) {
	doc := minValidYAML("v1") + "typo_field: 1\n"
	if _, err := ParseDocument([]byte(doc), FormatYAML); err == nil {
		t.Fatal("未知字段应当被拒绝")
	}
}

// TestParseDocumentUnsupportedFormat 验证格式错误可被区分出来。
//
// 控制面靠这个把 415 和 400 分开：改 Content-Type 能解决 vs 要改配置内容。
func TestParseDocumentUnsupportedFormat(t *testing.T) {
	_, err := ParseDocument([]byte("x: 1"), "toml")
	if err == nil {
		t.Fatal("不支持的格式应当报错")
	}
	if !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("错误应可被 errors.Is(ErrUnsupportedFormat) 识别，实际: %v", err)
	}
}

// stubClient 是只返回固定内容的最小 ConfigClient。
type stubClient struct{ content string }

func (s *stubClient) Type() string { return "stub" }

func (s *stubClient) Get(context.Context, string, string) string { return s.content }

func (s *stubClient) OnChange(context.Context, string, func(string)) {}

func (s *stubClient) Close() error { return nil }
