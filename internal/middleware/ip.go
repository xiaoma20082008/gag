package middleware

import (
	"net/netip"
	"strings"

	"gag/internal/core"
	gerr "gag/pkg/errors"
)

// IP 按客户端 IP 做准入控制。
type IP struct {
	allow []netip.Prefix
	deny  []netip.Prefix
}

// NewIP 创建 IP 黑白名单中间件；两张单都为空时返回 nil（不挂载）。
func NewIP(allow, deny []string) core.Module {
	m := &IP{allow: parsePrefixes(allow), deny: parsePrefixes(deny)}
	if len(m.allow) == 0 && len(m.deny) == 0 {
		return nil
	}
	return m
}

func (m *IP) Name() string { return "mw_ip" }

func (m *IP) Init(cbs *core.Callbacks) error {
	cbs.Add(core.HookBeforeRoute, 30, "mw_ip", "ip.filter", m.handle)
	return nil
}

func (m *IP) handle(c *core.RequestContext) core.Action {
	ip, err := netip.ParseAddr(c.ClientIP)
	if err != nil {
		// 解析不出地址（unix socket、带 zone 的 IPv6）时不猜，交给后续治理。
		return core.ActionGoOn
	}
	// 黑名单优先：显式拒绝不应被白名单的宽松规则救回来。
	for _, p := range m.deny {
		if p.Contains(ip) {
			c.Fail(gerr.New(gerr.CodePermissionDenied, "客户端 IP 被拒绝访问"))
			return core.ActionFinish
		}
	}
	if len(m.allow) > 0 {
		for _, p := range m.allow {
			if p.Contains(ip) {
				return core.ActionGoOn
			}
		}
		c.Fail(gerr.New(gerr.CodePermissionDenied, "客户端 IP 不在白名单"))
		return core.ActionFinish
	}
	return core.ActionGoOn
}

// parsePrefixes 解析 IP 列表，同时接受 CIDR 与裸地址。
//
// 裸地址按 /32 或 /128 处理：写成 10.0.0.1 的运维意图显然是"这一台"。
func parsePrefixes(list []string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(list))
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if p, err := netip.ParsePrefix(item); err == nil {
			out = append(out, p)
			continue
		}
		if addr, err := netip.ParseAddr(item); err == nil {
			out = append(out, netip.PrefixFrom(addr, addr.BitLen()))
		}
	}
	return out
}
