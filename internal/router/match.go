package router

import (
	"strings"

	"gag/internal/config"
	"gag/internal/core"
)

// Matcher 是一条路由的预编译匹配器。
type Matcher struct {
	path    string
	prefix  bool
	methods map[string]struct{}
	host    string
	headers map[string]string
	query   map[string]string
	score   int
}

// NewMatcher 编译路由匹配条件。
func NewMatcher(r *config.Route) *Matcher {
	m := &Matcher{
		path:    r.Path,
		host:    strings.ToLower(r.Host),
		headers: r.Headers,
		query:   r.Query,
	}
	switch {
	case strings.HasSuffix(r.Path, "/*"):
		m.path = strings.TrimSuffix(r.Path, "/*")
		m.prefix = true
	case strings.HasSuffix(r.Path, "*"):
		m.path = strings.TrimSuffix(r.Path, "*")
		m.prefix = true
	}
	if len(r.Methods) > 0 {
		m.methods = make(map[string]struct{}, len(r.Methods))
		for _, method := range r.Methods {
			m.methods[strings.ToUpper(method)] = struct{}{}
		}
	}
	m.score = m.computeScore()
	return m
}

// computeScore 决定匹配优先级：精确路径 > 前缀路径 > 条件更多的路由。
func (m *Matcher) computeScore() int {
	score := len(m.path)
	if m.prefix {
		score += 1000
	} else {
		score += 10000
	}
	score += len(m.headers) * 100
	if m.host != "" {
		score += 500
	}
	if len(m.methods) > 0 {
		score += 50
	}
	return score
}

// Score 越大越优先。
func (m *Matcher) Score() int { return m.score }

// Match 判断请求是否命中该路由。
func (m *Matcher) Match(meta *core.RequestMeta) bool {
	if m.host != "" && !strings.EqualFold(m.host, meta.Host) {
		return false
	}
	if m.methods != nil {
		if _, ok := m.methods[strings.ToUpper(meta.Method)]; !ok {
			return false
		}
	}
	if m.prefix {
		if !strings.HasPrefix(meta.Path, m.path) {
			return false
		}
		// 避免 /api 前缀误命中 /apix。
		if len(meta.Path) > len(m.path) && !strings.HasSuffix(m.path, "/") && meta.Path[len(m.path)] != '/' {
			return false
		}
	} else if meta.Path != m.path {
		return false
	}
	for k, want := range m.headers {
		if meta.Header.Get(k) != want {
			return false
		}
	}
	for k, want := range m.query {
		if meta.Query.Get(k) != want {
			return false
		}
	}
	return true
}
