package admin

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	"gag/internal/config"
	"gag/internal/controlplane"
)

func (admin *AdminServer) registerRouters() {
	// 探针与指标：供 K8s / Prometheus 直接抓取，不做鉴权。
	admin.registerGinGet("/healthz", admin.healthz)
	admin.registerGinGet("/readyz", admin.readiness)
	admin.registerGinGet("/metrics", admin.metrics)

	// 控制面接口：会改变数据面行为，必须鉴权。
	admin.registerGinGet("/admin/status", admin.guard(admin.status))
	admin.registerGinGet("/admin/config", admin.guard(admin.getConfig))
	admin.registerGinPost("/admin/config", admin.guard(admin.publishConfig))
	admin.registerGinPost("/admin/rollback", admin.guard(admin.rollback))
}

func (admin *AdminServer) healthz(c *gin.Context) {
	c.String(http.StatusOK, "ok")
}

// readiness 反映数据面是否已装载配置：进程活着但配置没加载完时不应接流量。
func (admin *AdminServer) readiness(c *gin.Context) {
	if admin.deps.Ready != nil {
		if err := admin.deps.Ready(); err != nil {
			c.String(http.StatusServiceUnavailable, "not ready: "+err.Error())
			return
		}
	}
	c.String(http.StatusOK, "ready")
}

func (admin *AdminServer) metrics(c *gin.Context) {
	if admin.deps.Metrics != nil {
		admin.deps.Metrics.ServeHTTP(c.Writer, c.Request)
		return
	}
	c.String(http.StatusOK, "# metrics collector not configured\n")
}

func (admin *AdminServer) guard(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !admin.authorized(c.Request) {
			c.Header("WWW-Authenticate", `Bearer realm="gag-admin"`)
			writeJSON(c, http.StatusUnauthorized, gin.H{"error": "admin credential required"})
			return
		}
		next(c)
	}
}

func (admin *AdminServer) authorized(r *http.Request) bool {
	if admin.deps.Token == "" {
		return isLoopback(r.RemoteAddr)
	}
	const prefix = "Bearer "
	header := r.Header.Get("Authorization")
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(got), []byte(admin.deps.Token)) == 1
}

func (admin *AdminServer) status(c *gin.Context) {
	out := gin.H{
		"control_plane": admin.deps.Store != nil,
	}
	if admin.deps.Store != nil {
		if cur := admin.deps.Store.Current(); cur != nil {
			out["revision"] = cur.ID
			out["version"] = cur.Version
			out["hash"] = cur.Hash
			out["author"] = cur.Author
			out["comment"] = cur.Comment
			out["published_at"] = cur.CreatedAt
		}
		out["history"] = len(admin.deps.Store.History())
	}
	if admin.deps.Info != nil {
		for k, v := range admin.deps.Info() {
			out[k] = v
		}
	}
	writeJSON(c, http.StatusOK, out)
}

func (admin *AdminServer) getConfig(c *gin.Context) {
	if admin.deps.Store == nil {
		writeJSON(c, http.StatusServiceUnavailable, gin.H{"error": "control plane not configured"})
		return
	}
	cur := admin.deps.Store.Current()
	if cur == nil || cur.Snapshot == nil {
		writeJSON(c, http.StatusNotFound, gin.H{"error": "no config published"})
		return
	}
	data, err := yaml.Marshal(cur.Snapshot.Config())
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", data)
}

func (admin *AdminServer) publishConfig(c *gin.Context) {
	if admin.deps.Store == nil {
		writeJSON(c, http.StatusServiceUnavailable, gin.H{"error": "control plane not configured"})
		return
	}
	body, err := c.GetRawData()
	if err != nil {
		writeJSON(c, http.StatusBadRequest, gin.H{"error": "读取请求体失败: " + err.Error()})
		return
	}
	cfg, err := config.Load(bytes.NewReader(body))
	if err != nil {
		// 校验失败必须挡在这里：一旦进 Store，数据面就会整体拒绝热加载。
		writeJSON(c, http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	author := headerOr(c, "X-Admin-User", "api")
	comment := c.Query("comment")
	if comment == "" {
		comment = c.GetHeader("X-Admin-Comment")
	}
	ver, err := admin.deps.Store.Publish(cfg, author, comment)
	if err != nil {
		writeJSON(c, http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	writeJSON(c, http.StatusOK, gin.H{
		"revision": ver.ID,
		"version":  ver.Version,
		"hash":     ver.Hash,
		"author":   ver.Author,
	})
}

// rollback 回滚到指定修订号。
func (admin *AdminServer) rollback(c *gin.Context) {
	if admin.deps.Store == nil {
		writeJSON(c, http.StatusServiceUnavailable, gin.H{"error": "control plane not configured"})
		return
	}
	id, err := strconv.ParseInt(c.Query("id"), 10, 64)
	if err != nil {
		writeJSON(c, http.StatusBadRequest, gin.H{"error": "id 必须是整数修订号"})
		return
	}
	ver, err := admin.deps.Store.Rollback(id, headerOr(c, "X-Admin-User", "api"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, controlplane.ErrVersionNotFound) {
			status = http.StatusNotFound
		}
		writeJSON(c, status, gin.H{"error": err.Error()})
		return
	}
	writeJSON(c, http.StatusOK, gin.H{
		"revision": ver.ID,
		"version":  ver.Version,
		"hash":     ver.Hash,
	})
}

func headerOr(c *gin.Context, name, fallback string) string {
	if v := strings.TrimSpace(c.GetHeader(name)); v != "" {
		return v
	}
	return fallback
}

func writeJSON(c *gin.Context, status int, payload any) {
	c.Header("X-Content-Type-Options", "nosniff")
	c.JSON(status, payload)
}

func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
