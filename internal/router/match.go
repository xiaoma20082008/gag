package router

import (
	"strings"

	"gag/internal/config"
	"gag/internal/core"
	"gag/internal/script"
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
	// rule 是编译后的 DSL 表达式；为 nil 表示这条路由没有额外条件。
	rule *script.Script
}

func NewMatcher(r *config.Route) (*Matcher, error) {
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
	return m, nil
}

// computeScore 决定匹配优先级：精确路径 > 前缀路径 > 仅规则。
//
// 显式 Priority 不参与这里，它在外层排序时优先比较。
func (m *Matcher) computeScore() int {
	score := 0
	switch {
	case m.path == "":
		// 只写了 Rule：没有结构上的特异性，排在最后，
		// 让静态路由先有机会命中；需要抢在前面就配 priority。
	case m.prefix:
		score = 1000 + len(m.path)
	default:
		score = 10000 + len(m.path)
	}
	if m.host != "" {
		score += 500
	}
	score += len(m.headers) * 100
	if len(m.methods) > 0 {
		score += 50
	}
	if m.rule != nil {
		score += 200
	}
	return score
}

// Score 越大越优先。
func (m *Matcher) Score() int { return m.score }

// Match 判断请求是否命中该路由。
//
// env 由调用方按请求构造一次并复用：一条租户下可能有几十条路由，
// 每条都重新拼一遍 header/query 映射是白费。
func (m *Matcher) Match(meta *core.RequestMeta, env *script.Env) (bool, error) {
	if !m.matchStatic(meta) {
		return false, nil
	}
	if m.rule == nil {
		return true, nil
	}
	return true, nil
}

// matchStatic 执行 path/method/host/header/query 的快速筛选。
func (m *Matcher) matchStatic(meta *core.RequestMeta) bool {
	if m.host != "" && !strings.EqualFold(m.host, meta.Host) {
		return false
	}
	if m.methods != nil {
		if _, ok := m.methods[strings.ToUpper(meta.Method)]; !ok {
			return false
		}
	}
	if m.path != "" {
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
