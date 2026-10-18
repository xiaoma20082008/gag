package middleware

import (
	"encoding/json"
	"net/http"
	"strings"

	"gag/internal/core"
	gerr "gag/pkg/errors"
)

// Validate 校验 Content-Type、必填头/查询参数与必填 body 字段。
type Validate struct{ st Policies }

// NewValidate 创建参数校验中间件。
func NewValidate(st Policies) core.Module { return &Validate{st: st} }

func (m *Validate) Name() string { return "mw_validate" }

func (m *Validate) Init(cbs *core.Callbacks) error {
	cbs.Add(core.HookAfterRoute, 20, "mw_validate", "validate.request", m.handle)
	return nil
}

func (m *Validate) handle(c *core.RequestContext) core.Action {
	set := setOf(c, m.st)
	if set == nil || set.Validate == nil {
		return core.ActionGoOn
	}
	v := set.Validate

	// 先用 Content-Length 快速拒绝，不必把整个 body 读进来。
	if v.MaxBodyBytes > 0 && c.Request.ContentLength > v.MaxBodyBytes {
		c.Fail(gerr.Errorf(gerr.CodePayloadTooLarge,
			"请求体 %d 字节超过限制 %d", c.Request.ContentLength, v.MaxBodyBytes))
		return core.ActionFinish
	}
	if len(v.ContentTypes) > 0 && hasBody(c.Request) {
		if !matchContentType(c.Request.Header.Get("Content-Type"), v.ContentTypes) {
			c.Fail(gerr.New(gerr.CodeInvalidArgument, "不支持的 Content-Type"))
			return core.ActionFinish
		}
	}
	for _, name := range v.RequiredHeaders {
		if c.Request.Header.Get(name) == "" {
			c.Fail(gerr.Errorf(gerr.CodeInvalidArgument, "缺少必填请求头 %s", name))
			return core.ActionFinish
		}
	}
	query := c.Request.URL.Query()
	for _, name := range v.RequiredQuery {
		if !query.Has(name) {
			c.Fail(gerr.Errorf(gerr.CodeInvalidArgument, "缺少必填查询参数 %s", name))
			return core.ActionFinish
		}
	}
	if len(v.RequiredBodyFields) > 0 {
		return m.checkBodyFields(c, v.RequiredBodyFields)
	}
	return core.ActionGoOn
}

// checkBodyFields 校验 JSON body 必填字段。
//
// 只解析到 map[string]json.RawMessage：字段存在性判断不需要反序列化成结构体，
// 对超长 body 与嵌套结构都更稳。
func (m *Validate) checkBodyFields(c *core.RequestContext, fields []string) core.Action {
	data, err := core.ReadBody(c)
	if err != nil {
		c.Fail(err)
		return core.ActionFinish
	}
	if len(strings.TrimSpace(string(data))) == 0 {
		c.Fail(gerr.New(gerr.CodeInvalidArgument, "请求体为空，缺少必填字段"))
		return core.ActionFinish
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(data, &obj); err != nil {
		c.Fail(gerr.Errorf(gerr.CodeInvalidArgument, "请求体不是合法 JSON").WithCause(err))
		return core.ActionFinish
	}
	for _, f := range fields {
		if _, ok := obj[f]; !ok {
			c.Fail(gerr.Errorf(gerr.CodeInvalidArgument, "缺少必填字段 %s", f))
			return core.ActionFinish
		}
	}
	return core.ActionGoOn
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
