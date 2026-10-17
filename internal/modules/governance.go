package modules

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"gag/internal/gateway"
	"gag/internal/policy"
)

// AuthModule 在路由确定后校验调用方身份。
//
// 放在 AfterRoute 而不是 BeforeRoute：认证策略是按路由配置的，
// 不知道路由就不知道该用哪套凭证；代价是无法在路由前拦掉未认证流量。
type AuthModule struct{ st State }

// NewAuth 创建认证模块。
func NewAuth(st State) gateway.Module { return &AuthModule{st: st} }

func (m *AuthModule) Name() string { return "mod_auth" }

func (m *AuthModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookAfterRoute, 10, "mod_auth", "auth.authenticate", m.handle)
	return nil
}

func (m *AuthModule) handle(c *gateway.RequestContext) gateway.Action {
	set := setOf(c, m.st)
	if set == nil || set.Auth == nil {
		// 路由没配认证：放行，但在访问日志里留下"anonymous"而不是空值。
		c.AuthResult = "none"
		return gateway.ActionGoOn
	}

	principal, err := set.Auth.Authenticate(c.Request)
	if err != nil {
		c.AuthResult = "rejected"
		c.Fail(gateway.NewError(gateway.CodeUnauthenticated, "认证失败").WithCause(err))
		return gateway.ActionFinish
	}
	// 凭证自带的租户与请求租户不一致属于跨租户访问，与"凭证无效"分开报。
	if principal != nil && principal.Tenant != "" && principal.Tenant != c.Tenant {
		c.AuthResult = "tenant_mismatch"
		c.Fail(gateway.Errorf(gateway.CodePermissionDenied, "凭证不属于租户 %s", c.Tenant))
		return gateway.ActionFinish
	}
	for _, scope := range set.RequiredScopes {
		if !principal.HasScope(scope) {
			c.AuthResult = "scope_denied"
			c.Fail(gateway.Errorf(gateway.CodePermissionDenied, "缺少 scope %s", scope))
			return gateway.ActionFinish
		}
	}

	setPrincipal(c, principal)
	c.AuthResult = set.Auth.Type()
	return gateway.ActionGoOn
}

// ---------- 请求参数校验 ----------

// ValidateModule 校验 Content-Type、必填头/查询参数与必填 body 字段。
type ValidateModule struct{ st State }

// NewValidate 创建参数校验模块。
func NewValidate(st State) gateway.Module { return &ValidateModule{st: st} }

func (m *ValidateModule) Name() string { return "mod_validate" }

func (m *ValidateModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookAfterRoute, 20, "mod_validate", "validate.request", m.handle)
	return nil
}

func (m *ValidateModule) handle(c *gateway.RequestContext) gateway.Action {
	set := setOf(c, m.st)
	if set == nil || set.Validate == nil {
		return gateway.ActionGoOn
	}
	v := set.Validate

	// 先用 Content-Length 快速拒绝，不必把整个 body 读进来。
	if v.MaxBodyBytes > 0 && c.Request.ContentLength > v.MaxBodyBytes {
		c.Fail(gateway.Errorf(gateway.CodePayloadTooLarge,
			"请求体 %d 字节超过限制 %d", c.Request.ContentLength, v.MaxBodyBytes))
		return gateway.ActionFinish
	}
	if len(v.ContentTypes) > 0 && hasBody(c.Request) {
		if !matchContentType(c.Request.Header.Get("Content-Type"), v.ContentTypes) {
			c.Fail(gateway.Errorf(gateway.CodeInvalidArgument, "不支持的 Content-Type"))
			return gateway.ActionFinish
		}
	}
	for _, name := range v.RequiredHeaders {
		if c.Request.Header.Get(name) == "" {
			c.Fail(gateway.Errorf(gateway.CodeInvalidArgument, "缺少必填请求头 %s", name))
			return gateway.ActionFinish
		}
	}
	query := c.Request.URL.Query()
	for _, name := range v.RequiredQuery {
		if !query.Has(name) {
			c.Fail(gateway.Errorf(gateway.CodeInvalidArgument, "缺少必填查询参数 %s", name))
			return gateway.ActionFinish
		}
	}
	if len(v.RequiredBodyFields) > 0 {
		return m.checkBodyFields(c, v.RequiredBodyFields)
	}
	return gateway.ActionGoOn
}

// checkBodyFields 校验 JSON body 必填字段。
//
// 只解析到 map[string]json.RawMessage：字段存在性判断不需要反序列化成结构体，
// 对超长 body 与嵌套结构都更稳。
func (m *ValidateModule) checkBodyFields(c *gateway.RequestContext, fields []string) gateway.Action {
	data, err := readBody(c)
	if err != nil {
		c.Fail(err)
		return gateway.ActionFinish
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		c.Fail(gateway.NewError(gateway.CodeInvalidArgument, "请求体为空，缺少必填字段"))
		return gateway.ActionFinish
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		c.Fail(gateway.Errorf(gateway.CodeInvalidArgument, "请求体不是合法 JSON").WithCause(err))
		return gateway.ActionFinish
	}
	for _, f := range fields {
		if _, ok := obj[f]; !ok {
			c.Fail(gateway.Errorf(gateway.CodeInvalidArgument, "缺少必填字段 %s", f))
			return gateway.ActionFinish
		}
	}
	return gateway.ActionGoOn
}

// hasBody 判断请求是否带 body；ContentLength 为 -1（chunked）也算有。
func hasBody(r *http.Request) bool {
	if r.Body == nil || r.Body == http.NoBody {
		return false
	}
	return r.ContentLength != 0
}

// matchContentType 用前缀匹配比较媒体类型，忽略 charset 等参数。
func matchContentType(ct string, allowed []string) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.SplitN(ct, ";", 2)[0]))
	if ct == "" {
		return false
	}
	for _, a := range allowed {
		if strings.EqualFold(strings.TrimSpace(a), ct) {
			return true
		}
	}
	return false
}

// ---------- 限流 ----------

// RateLimitModule 按 ip/tenant/route/user 维度限流。
type RateLimitModule struct{ st State }

// NewRateLimit 创建限流模块。
func NewRateLimit(st State) gateway.Module { return &RateLimitModule{st: st} }

func (m *RateLimitModule) Name() string { return "mod_ratelimit" }

func (m *RateLimitModule) Init(cbs *gateway.Callbacks) error {
	cbs.Add(gateway.HookAfterRoute, 30, "mod_ratelimit", "ratelimit.allow", m.handle)
	return nil
}

func (m *RateLimitModule) handle(c *gateway.RequestContext) gateway.Action {
	set := setOf(c, m.st)
	if set == nil || set.RateLimit == nil {
		c.RateLimitResult = "unlimited"
		return gateway.ActionGoOn
	}

	key := m.keyFor(c, set)
	setRateLimitKey(c, key)
	ok, wait := set.RateLimit.Allow(key)
	if !ok {
		c.RateLimitResult = "rejected"
		c.Fail(gateway.NewError(gateway.CodeRateLimited, "请求过于频繁").
			WithRetryAfter(retryAfterFloor(wait)))
		return gateway.ActionFinish
	}
	c.RateLimitResult = "allowed"
	return gateway.ActionGoOn
}

// keyFor 按配置维度取限流键。
//
// user 维度依赖认证结果，因此只在已认证时有值；
// 未认证流量退化成 ip，避免把匿名攻击打成同一个桶互相连坐。
func (m *RateLimitModule) keyFor(c *gateway.RequestContext, set *policy.Set) string {
	switch set.RateLimitKey {
	case "ip":
		return c.ClientIP
	case "tenant":
		return c.Tenant
	case "user":
		if p := principalOf(c); p != nil && p.Subject != "" {
			return p.Subject
		}
		return c.ClientIP
	default:
		return c.RouteID
	}
}

// retryAfterFloor 把等待时间上调到整秒。
//
// Retry-After 的语义是秒级；向下取整会把 900ms 的等待说成"立刻重试"，
// 客户端按字面值重试只会再次被拒。
func retryAfterFloor(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	return time.Duration((int(d.Seconds()) + 1) * int(time.Second))
}
