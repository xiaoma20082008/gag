package logging

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("日志行不是 JSON: %q (%v)", line, err)
		}
		out = append(out, m)
	}
	return out
}

func TestRedaction(t *testing.T) {
	var buf bytes.Buffer
	log := New(Config{Writer: &buf, Level: LevelInfo})
	defer log.Close()

	log.Info("login", map[string]any{
		"authorization": "Bearer secret",
		"user":          "alice",
		"nested":        map[string]any{"api_key": "abc", "safe": 1},
	})

	lines := decodeLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("日志行数 = %d", len(lines))
	}
	if lines[0]["authorization"] != "***" {
		t.Fatalf("authorization 未脱敏: %v", lines[0]["authorization"])
	}
	nested, _ := lines[0]["nested"].(map[string]any)
	if nested["api_key"] != "***" {
		t.Fatalf("嵌套 api_key 未脱敏: %v", nested)
	}
	if nested["safe"] != float64(1) {
		t.Fatalf("非敏感字段被破坏: %v", nested["safe"])
	}
	if lines[0]["user"] != "alice" {
		t.Fatalf("普通字段被改写: %v", lines[0]["user"])
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log := New(Config{Writer: &buf, Level: LevelWarn})
	defer log.Close()

	log.Debug("debug", nil)
	log.Info("info", nil)
	log.Warn("warn", nil)
	log.Error("error", nil)

	lines := decodeLines(t, &buf)
	if len(lines) != 2 {
		t.Fatalf("期望 2 条日志，实际 %d: %s", len(lines), buf.String())
	}
	if lines[0]["level"] != "warn" || lines[1]["level"] != "error" {
		t.Fatalf("级别过滤异常: %v", lines)
	}
}

func TestAsyncLoggerFlush(t *testing.T) {
	var buf bytes.Buffer
	log := New(Config{Writer: &buf, Level: LevelInfo, Async: true, QueueSize: 8})
	for i := 0; i < 4; i++ {
		log.Info("async", map[string]any{"i": i})
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	if got := len(decodeLines(t, &buf)); got != 4 {
		t.Fatalf("异步日志未全部落盘: %d", got)
	}
}

func TestAccessEntry(t *testing.T) {
	var buf bytes.Buffer
	log := New(Config{Writer: &buf, Level: LevelInfo})
	defer log.Close()

	log.Access(AccessEntry{RequestID: "r1", Route: "api", Status: 200, LatencyMS: 1.5})
	lines := decodeLines(t, &buf)
	if len(lines) != 1 {
		t.Fatalf("访问日志行数 = %d", len(lines))
	}
	if lines[0]["request_id"] != "r1" || lines[0]["status_code"] != float64(200) {
		t.Fatalf("访问日志字段异常: %v", lines[0])
	}
}
