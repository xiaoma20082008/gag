package server

import (
	"encoding/json"
	"net/http"
)

// chainHandler 把请求转交给"当前生效"的治理链。
//
// 中间这层间接是热加载的前提：监听中的 http.Server 不能更换 Handler，
// 但配置发布需要替换整条治理链。请求每次进来都重新读一次原子指针，
// 因此新配置对下一个请求立即生效，在途请求仍跑完旧链。
type chainHandler struct {
	srv *GagServer
}

func (h *chainHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ch := h.srv.chain.Load()
	if ch == nil {
		writeUnavailable(w)
		return
	}
	ch.ServeHTTP(w, r)
}

// writeUnavailable 在配置尚未装载时返回与治理链一致的错误结构，
// 避免客户端在不同阶段看到两套响应格式。
func writeUnavailable(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusServiceUnavailable)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"code":    "upstream_unavailable",
		"message": "gateway is starting",
	})
}
