// Package tracing 提供最小可用的链路追踪模型（W3C traceparent 兼容）。
//
// 骨架阶段不引入 OpenTelemetry SDK：网关只需要生成/透传上下文并记录
// 阶段耗时，导出交给后续接入的 OTLP exporter。
package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Header 是 W3C Trace Context 的标准头。
const Header = "traceparent"

// Span 表示一个处理阶段。
type Span struct {
	TraceID  string
	SpanID   string
	ParentID string
	Name     string
	Start    time.Time

	mu    sync.Mutex
	end   time.Time
	attrs map[string]string
}

type spanKey struct{}

// NewTraceID 生成 32 位十六进制 trace id。
func NewTraceID() string { return randomHex(16) }

// NewSpanID 生成 16 位十六进制 span id。
func NewSpanID() string { return randomHex(8) }

func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 不可用时退化为时间戳，保证请求不会因 id 生成失败而中断。
		now := uint64(time.Now().UnixNano())
		for i := range buf {
			buf[i] = byte(now >> (8 * uint(i%8)))
		}
	}
	return hex.EncodeToString(buf)
}

// Extract 从入站请求中解析 traceparent；格式非法时返回空值。
func Extract(h http.Header) (traceID, parentSpanID string) {
	raw := strings.TrimSpace(h.Get(Header))
	if raw == "" {
		return "", ""
	}
	parts := strings.Split(raw, "-")
	if len(parts) != 4 || len(parts[1]) != 32 || len(parts[2]) != 16 {
		return "", ""
	}
	if _, err := hex.DecodeString(parts[1]); err != nil {
		return "", ""
	}
	if _, err := hex.DecodeString(parts[2]); err != nil {
		return "", ""
	}
	return parts[1], parts[2]
}

// Inject 把当前链路写入出站请求头。
func Inject(h http.Header, traceID, spanID string) {
	if traceID == "" || spanID == "" {
		return
	}
	h.Set(Header, "00-"+traceID+"-"+spanID+"-01")
}

// StartSpan 在 ctx 上创建 span；若 ctx 中已有 trace 则继承 trace id。
func StartSpan(ctx context.Context, name string) (context.Context, *Span) {
	parent := SpanFromContext(ctx)
	s := &Span{
		Name:  name,
		Start: time.Now(),
		attrs: make(map[string]string, 4),
	}
	if parent != nil {
		s.TraceID = parent.TraceID
		s.ParentID = parent.SpanID
	} else if traceID, parentID := traceFromContext(ctx); traceID != "" {
		s.TraceID = traceID
		s.ParentID = parentID
	}
	if s.TraceID == "" {
		s.TraceID = NewTraceID()
	}
	s.SpanID = NewSpanID()
	return context.WithValue(ctx, spanKey{}, s), s
}

type traceInfo struct{ traceID, spanID string }

type traceKey struct{}

// WithTrace 把外部解析出的 trace 信息注入 context（用于没有 span 的入口）。
func WithTrace(ctx context.Context, traceID, parentSpanID string) context.Context {
	return context.WithValue(ctx, traceKey{}, traceInfo{traceID: traceID, spanID: parentSpanID})
}

func traceFromContext(ctx context.Context) (string, string) {
	if ctx == nil {
		return "", ""
	}
	if info, ok := ctx.Value(traceKey{}).(traceInfo); ok {
		return info.traceID, info.spanID
	}
	return "", ""
}

// SpanFromContext 取出当前 span。
func SpanFromContext(ctx context.Context) *Span {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(spanKey{}).(*Span)
	return s
}

// End 结束 span；重复调用无副作用。
func (s *Span) End() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.end.IsZero() {
		s.end = time.Now()
	}
	s.mu.Unlock()
}

// Duration 返回 span 耗时。
func (s *Span) Duration() time.Duration {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	end := s.end
	if end.IsZero() {
		end = time.Now()
	}
	return end.Sub(s.Start)
}

// SetAttr 附加属性。
func (s *Span) SetAttr(key, value string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.attrs[key] = value
	s.mu.Unlock()
}

// Attributes 返回属性快照。
func (s *Span) Attributes() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.attrs))
	for k, v := range s.attrs {
		out[k] = v
	}
	return out
}

// Sampled 基于 trace id 做确定性采样，保证同一链路在多个实例上判断一致。
func Sampled(traceID string, rate float64) bool {
	if rate >= 1 {
		return true
	}
	if rate <= 0 {
		return false
	}
	if len(traceID) < 8 {
		return false
	}
	var v uint64
	for _, c := range traceID[:8] {
		v = v*16 + uint64(hexVal(c))
	}
	return float64(v%10000)/10000.0 < rate
}

func hexVal(c rune) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return 0
}
