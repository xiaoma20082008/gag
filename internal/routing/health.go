package routing

import (
	"sync"
)

// Health 维护实例健康状态。
//
// 状态由健康检查模块写入，路由层只读。未登记的实例默认视为健康，
// 避免启动瞬间（健康检查尚未跑完）把所有流量拒掉。
type Health struct {
	mu   sync.RWMutex
	down map[string]bool
}

// NewHealth 创建健康状态表。
func NewHealth() *Health {
	return &Health{down: make(map[string]bool, 32)}
}

// Key 生成实例的全局唯一键。
func Key(tenant, service, cluster, addr string) string {
	return tenant + "/" + service + "/" + cluster + "/" + addr
}

// Healthy 判断实例是否健康。
func (h *Health) Healthy(key string) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return !h.down[key]
}

// MarkDown 标记实例不可用。
func (h *Health) MarkDown(key string) {
	h.mu.Lock()
	h.down[key] = true
	h.mu.Unlock()
}

// MarkUp 标记实例恢复。
func (h *Health) MarkUp(key string) {
	h.mu.Lock()
	delete(h.down, key)
	h.mu.Unlock()
}

// Down 返回所有不健康实例，供运维接口展示。
func (h *Health) Down() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]string, 0, len(h.down))
	for k := range h.down {
		out = append(out, k)
	}
	return out
}
