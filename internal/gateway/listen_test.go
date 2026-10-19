package gateway

import "testing"

// TestSplitListen 覆盖地址拆分与校验。
//
// 这一步失败必须是启动失败：端口写错却延迟到 Listen 才报，
// 运维看到的会是"进程起来了但没有监听"，比直接退出难查得多。
func TestSplitListen(t *testing.T) {
	ok := []struct {
		addr string
		host string
		port int
	}{
		{":8080", "", 8080},
		{"127.0.0.1:9090", "127.0.0.1", 9090},
		{"0.0.0.0:80", "0.0.0.0", 80},
		{"[::1]:8081", "::1", 8081},
	}
	for _, c := range ok {
		host, port, err := splitListen(c.addr)
		if err != nil {
			t.Errorf("%q 应当合法，却报错: %v", c.addr, err)
			continue
		}
		if host != c.host || port != c.port {
			t.Errorf("%q 拆成 (%q,%d)，期望 (%q,%d)", c.addr, host, port, c.host, c.port)
		}
	}

	bad := []string{
		"",           // 空
		"8080",       // 没有 host:port 结构
		"localhost",  // 同上
		":",          // 端口为空
		":99999",     // 端口越界
		":-1",        // 负数
		":http",      // 非数字
		"127.0.0.1:", // 端口为空
	}
	for _, addr := range bad {
		if _, _, err := splitListen(addr); err == nil {
			t.Errorf("%q 应当被拒绝", addr)
		}
	}
}

// TestBuildString 验证构建标识的展示形式。
//
// 默认值是 "dev"/"unknown"——未经 build.sh 直接 go run 时要能一眼看出来。
func TestBuildString(t *testing.T) {
	cases := []struct {
		b    Build
		want string
	}{
		{Build{Version: "v1.2.3", Commit: "abc1234"}, "v1.2.3 (abc1234)"},
		{Build{Version: "v1.2.3"}, "v1.2.3"},
		{Build{Version: "v1.2.3", Commit: "unknown"}, "v1.2.3"},
		{Build{Version: "dev"}, "dev"},
	}
	for _, c := range cases {
		if got := c.b.String(); got != c.want {
			t.Errorf("%+v -> %q，期望 %q", c.b, got, c.want)
		}
	}
}

// TestBuildInfoDefaults 确认没注入时不会出现空字符串。
func TestBuildInfoDefaults(t *testing.T) {
	b := BuildInfo()
	if b.Version == "" || b.Commit == "" || b.Time == "" {
		t.Fatalf("构建标识不应有空字段: %+v", b)
	}
	if b.Version != "dev" {
		t.Logf("版本已被 -ldflags 注入: %s", b.Version)
	}
}
