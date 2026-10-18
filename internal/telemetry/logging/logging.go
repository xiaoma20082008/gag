// Package logging 提供结构化 JSON 日志，支持异步写入与敏感字段脱敏。
package logging

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Level 是日志级别。
type Level int8

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// ParseLevel 解析级别名，未知值回退到 info。
func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "info"
	}
}

// Config 是 Logger 的构造参数。
type Config struct {
	Writer     io.Writer
	Level      Level
	Async      bool
	QueueSize  int
	RedactKeys []string
}

// 默认脱敏字段（大小写不敏感，做子串匹配）。
var defaultRedactKeys = []string{
	"authorization", "cookie", "set-cookie", "password", "passwd",
	"secret", "token", "api_key", "apikey", "credential", "private_key",
}

// Logger 是并发安全的 JSON logger。
type Logger struct {
	level  Level
	writer io.Writer
	base   map[string]any
	redact []string

	ch        chan []byte
	wg        sync.WaitGroup
	mu        sync.Mutex
	closed    bool
	closeOnce sync.Once
	root      *Logger
	dropped   atomic.Uint64
}

// New 创建 logger。Async 为 true 时日志写入独立 goroutine，
// 队列满时丢弃并计数，避免日志阻塞请求路径。
func New(cfg Config) *Logger {
	if cfg.Writer == nil {
		cfg.Writer = os.Stdout
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 4096
	}
	redact := append(append([]string{}, defaultRedactKeys...), cfg.RedactKeys...)
	l := &Logger{
		level:  cfg.Level,
		writer: cfg.Writer,
		base:   map[string]any{},
		redact: redact,
	}
	l.root = l
	if cfg.Async {
		l.ch = make(chan []byte, cfg.QueueSize)
		l.wg.Add(1)
		go l.drain()
	}
	return l
}

func (l *Logger) drain() {
	defer l.wg.Done()
	for line := range l.ch {
		l.write(line)
	}
}

func (l *Logger) write(line []byte) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.writer.Write(line)
	_, _ = l.writer.Write([]byte("\n"))
}

// With 派生带固定字段的 logger，共享同一输出管道。
func (l *Logger) With(fields map[string]any) *Logger {
	if len(fields) == 0 {
		return l
	}
	base := make(map[string]any, len(l.base)+len(fields))
	for k, v := range l.base {
		base[k] = v
	}
	for k, v := range fields {
		base[k] = v
	}
	child := &Logger{
		level:  l.level,
		writer: l.writer,
		base:   base,
		redact: l.redact,
		root:   l.root,
	}
	return child
}

// Debug 输出 debug 日志。
func (l *Logger) Debug(msg string, fields map[string]any) { l.log(LevelDebug, msg, fields) }

// Info 输出 info 日志。
func (l *Logger) Info(msg string, fields map[string]any) { l.log(LevelInfo, msg, fields) }

// Warn 输出 warn 日志。
func (l *Logger) Warn(msg string, fields map[string]any) { l.log(LevelWarn, msg, fields) }

// Error 输出 error 日志。
func (l *Logger) Error(msg string, fields map[string]any) { l.log(LevelError, msg, fields) }

func (l *Logger) log(level Level, msg string, fields map[string]any) {
	if level < l.level {
		return
	}
	entry := make(map[string]any, len(l.base)+len(fields)+4)
	for k, v := range l.base {
		entry[k] = v
	}
	for k, v := range fields {
		entry[k] = v
	}
	entry["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	entry["level"] = level.String()
	entry["msg"] = msg

	line, err := json.Marshal(redactValue(entry, l.redact, 0))
	if err != nil {
		line, _ = json.Marshal(map[string]any{
			"ts":    time.Now().UTC().Format(time.RFC3339Nano),
			"level": "error",
			"msg":   "日志序列化失败",
			"error": err.Error(),
		})
	}
	l.emit(line)
}

func (l *Logger) emit(line []byte) {
	root := l.root
	if root.ch == nil {
		root.write(line)
		return
	}
	root.mu.Lock()
	defer root.mu.Unlock()
	if root.closed {
		return
	}
	select {
	case root.ch <- line:
	default:
		root.dropped.Add(1)
	}
}

// Dropped 返回因队列满被丢弃的日志条数，应作为指标上报。
func (l *Logger) Dropped() uint64 { return l.root.dropped.Load() }

// Close 关闭异步管道并 flush。
func (l *Logger) Close() error {
	root := l.root
	root.closeOnce.Do(func() {
		if root.ch != nil {
			root.mu.Lock()
			root.closed = true
			close(root.ch)
			root.mu.Unlock()
			root.wg.Wait()
			return
		}
		root.mu.Lock()
		root.closed = true
		root.mu.Unlock()
	})
	return nil
}

// AccessEntry 是访问日志的固定结构（对应设计文档 §5.3 的字段清单）。
type AccessEntry struct {
	Timestamp  string  `json:"ts"`
	TraceID    string  `json:"trace_id"`
	RequestID  string  `json:"request_id"`
	ClientIP   string  `json:"client_ip"`
	Tenant     string  `json:"tenant"`
	Route      string  `json:"route"`
	Service    string  `json:"service"`
	Cluster    string  `json:"cluster"`
	Instance   string  `json:"instance"`
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	Status     int     `json:"status_code"`
	LatencyMS  float64 `json:"latency_ms"`
	UpstreamMS float64 `json:"upstream_ms"`
	Attempts   int     `json:"attempts"`
	AuthResult string  `json:"auth_result"`
	RateLimit  string  `json:"rate_limit_result"`
	Circuit    string  `json:"circuit_state"`
	ErrorCode  string  `json:"error_code,omitempty"`
	UserAgent  string  `json:"user_agent,omitempty"`
}

// Access 输出一条访问日志。
func (l *Logger) Access(e AccessEntry) {
	if l.level > LevelInfo {
		return
	}
	if e.Timestamp == "" {
		e.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	l.emit(line)
}

// redactValue 递归脱敏 map/slice 中的敏感键，最多下探两层。
func redactValue(v any, keys []string, depth int) any {
	if depth > 2 {
		return v
	}
	switch val := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(val))
		for k, item := range val {
			if isSensitive(k, keys) {
				out[k] = "***"
				continue
			}
			out[k] = redactValue(item, keys, depth+1)
		}
		return out
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = redactValue(item, keys, depth+1)
		}
		return out
	default:
		return v
	}
}

func isSensitive(key string, keys []string) bool {
	lower := strings.ToLower(key)
	for _, k := range keys {
		if strings.Contains(lower, k) {
			return true
		}
	}
	return false
}

// String 便于调试。
func (l *Logger) String() string { return fmt.Sprintf("logger(level=%s)", l.level) }
