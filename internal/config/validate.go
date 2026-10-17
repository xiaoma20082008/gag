package config

import (
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
)

// ValidationError 汇总一次校验中的所有问题，便于控制面一次性反馈。
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("配置校验失败(%d 项): %s", len(e.Problems), strings.Join(e.Problems, "; "))
}

func (e *ValidationError) add(format string, args ...any) {
	e.Problems = append(e.Problems, fmt.Sprintf(format, args...))
}

// Validate 执行结构性与语义校验。
//
// 校验分两层：这里负责"单份配置自洽"（引用存在、取值合法、唯一性）；
// 跨版本的兼容性校验由控制面在 Publish 时补充。
func Validate(c *Config) error {
	errs := &ValidationError{}
	if c == nil {
		errs.add("配置为 nil")
		return errs
	}

	if len(c.Tenants) == 0 {
		errs.add("tenants 不能为空")
	}

	if err := validateListenAddr("listen", c.Server.Listen); err != nil {
		errs.add("%s", err)
	}
	if err := validateListenAddr("admin_listen", c.Server.AdminListen); err != nil {
		errs.add("%s", err)
	}
	if listenConflict(c.Server.Listen, c.Server.AdminListen) {
		errs.add("listen(%s) 与 admin_listen(%s) 冲突：业务端口与管理端口必须分开",
			c.Server.Listen, c.Server.AdminListen)
	}

	tenantNames := map[string]bool{}
	routeIDs := map[string]string{} // routeID -> tenant/service
	for ti, t := range c.Tenants {
		where := fmt.Sprintf("tenants[%d]", ti)
		if t.Name == "" {
			errs.add("%s.name 不能为空", where)
			continue
		}
		if tenantNames[t.Name] {
			errs.add("租户 %q 重复", t.Name)
		}
		tenantNames[t.Name] = true
		where = fmt.Sprintf("tenant(%s)", t.Name)

		if len(t.Services) == 0 {
			errs.add("%s 未定义任何 service", where)
		}
		serviceNames := map[string]bool{}
		for si, s := range t.Services {
			swhere := fmt.Sprintf("%s.services[%d]", where, si)
			if s.Name == "" {
				errs.add("%s.name 不能为空", swhere)
				continue
			}
			if serviceNames[s.Name] {
				errs.add("%s 中 service %q 重复", where, s.Name)
			}
			serviceNames[s.Name] = true
			swhere = fmt.Sprintf("%s.service(%s)", where, s.Name)

			switch s.Protocol {
			case "http", "grpc", "ai":
			default:
				errs.add("%s 的 protocol %q 非法（http|grpc|ai）", swhere, s.Protocol)
			}
			if s.Timeout < 0 {
				errs.add("%s 的 timeout 不能为负", swhere)
			}
			validatePolicies(errs, swhere, s.Auth, s.Validate, s.RateLimit, s.Breaker, s.Retry)

			if len(s.Clusters) == 0 {
				errs.add("%s 未定义 cluster", swhere)
			}
			clusterNames := map[string]bool{}
			for ci, cl := range s.Clusters {
				cwhere := fmt.Sprintf("%s.clusters[%d]", swhere, ci)
				if cl.Name == "" {
					errs.add("%s.name 不能为空", cwhere)
					continue
				}
				if clusterNames[cl.Name] {
					errs.add("%s 中 cluster %q 重复", swhere, cl.Name)
				}
				clusterNames[cl.Name] = true
				cwhere = fmt.Sprintf("%s.cluster(%s)", swhere, cl.Name)

				switch cl.LB {
				case "round_robin", "random", "least_conn", "consistent_hash":
				default:
					errs.add("%s 的 lb %q 非法（round_robin|random|least_conn|consistent_hash）", cwhere, cl.LB)
				}
				if len(cl.Instances) == 0 {
					errs.add("%s 未定义 instance", cwhere)
				}
				for ii, ins := range cl.Instances {
					if ins.Addr == "" {
						errs.add("%s.instances[%d].addr 不能为空", cwhere, ii)
						continue
					}
					if _, _, err := net.SplitHostPort(ins.Addr); err != nil {
						errs.add("%s.instances[%d].addr %q 需要 host:port 形式: %v", cwhere, ii, ins.Addr, err)
					}
				}
			}

			if len(s.Routes) == 0 {
				errs.add("%s 未定义 route", swhere)
			}
			for ri, r := range s.Routes {
				rwhere := fmt.Sprintf("%s.routes[%d]", swhere, ri)
				if r.ID == "" {
					errs.add("%s.id 不能为空", rwhere)
					continue
				}
				if prev, ok := routeIDs[r.ID]; ok {
					errs.add("route id %q 重复（已存在 %s）", r.ID, prev)
				}
				routeIDs[r.ID] = fmt.Sprintf("%s/%s", t.Name, s.Name)
				rwhere = fmt.Sprintf("%s.route(%s)", swhere, r.ID)

				if r.Path == "" {
					errs.add("%s.path 不能为空", rwhere)
				} else if !strings.HasPrefix(r.Path, "/") {
					errs.add("%s.path 必须以 / 开头", rwhere)
				}
				for _, m := range r.Methods {
					switch strings.ToUpper(m) {
					case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
					default:
						errs.add("%s 的 method %q 非法", rwhere, m)
					}
				}
				validatePolicies(errs, rwhere, r.Auth, r.Validate, r.RateLimit, r.Breaker, r.Retry)

				targets := 0
				if r.Cluster != "" {
					targets++
					if !clusterNames[r.Cluster] {
						errs.add("%s 引用了不存在的 cluster %q", rwhere, r.Cluster)
					}
				}
				for _, sp := range r.Split {
					targets++
					if sp.Cluster == "" {
						errs.add("%s.split 中存在空 cluster", rwhere)
					} else if !clusterNames[sp.Cluster] {
						errs.add("%s.split 引用了不存在的 cluster %q", rwhere, sp.Cluster)
					}
					if sp.Weight < 0 {
						errs.add("%s.split 的 weight 不能为负", rwhere)
					}
					if sp.Match != nil && (sp.Match.Percentage < 0 || sp.Match.Percentage > 100) {
						errs.add("%s.split 的 percentage 必须在 0-100 之间", rwhere)
					}
				}
				if targets == 0 {
					errs.add("%s 未指定 cluster 或 split", rwhere)
				}
			}
		}
	}

	if c.Server.DefaultTenant != "" && !tenantNames[c.Server.DefaultTenant] {
		errs.add("server.default_tenant %q 不存在", c.Server.DefaultTenant)
	}
	for _, cidr := range append(append([]string{}, c.Server.IPAllow...), c.Server.IPDeny...) {
		if _, _, err := net.ParseCIDR(cidr); err != nil && net.ParseIP(cidr) == nil {
			errs.add("IP 规则 %q 既不是合法 IP 也不是 CIDR", cidr)
		}
	}

	if len(errs.Problems) > 0 {
		sort.Strings(errs.Problems)
		return errs
	}
	return nil
}

func validatePolicies(errs *ValidationError, where string, auth *AuthPolicy, v *ValidatePolicy, rl *RateLimitPolicy, br *BreakerPolicy, rt *RetryPolicy) {
	if auth != nil {
		switch auth.Type {
		case "none", "api_key", "jwt":
		default:
			errs.add("%s 的 auth.type %q 非法（none|api_key|jwt）", where, auth.Type)
		}
		if auth.Type == "api_key" && len(auth.Keys) == 0 {
			errs.add("%s 使用 api_key 认证但未配置 keys", where)
		}
		if auth.Type == "jwt" && auth.Secret == "" {
			errs.add("%s 使用 jwt 认证但未配置 secret", where)
		}
	}
	if v != nil {
		if v.MaxBodyBytes < 0 {
			errs.add("%s 的 validate.max_body_bytes 不能为负", where)
		}
	}
	if rl != nil {
		if rl.RPS <= 0 {
			errs.add("%s 的 rate_limit.rps 必须大于 0", where)
		}
		if rl.Burst < 0 {
			errs.add("%s 的 rate_limit.burst 不能为负", where)
		}
		switch rl.Key {
		case "ip", "tenant", "route", "user":
		default:
			errs.add("%s 的 rate_limit.key %q 非法（ip|tenant|route|user）", where, rl.Key)
		}
		switch rl.Algorithm {
		case "token_bucket", "sliding_window":
		default:
			errs.add("%s 的 rate_limit.algorithm %q 非法（token_bucket|sliding_window）", where, rl.Algorithm)
		}
	}
	if br != nil {
		if br.ErrorRate <= 0 || br.ErrorRate > 1 {
			errs.add("%s 的 breaker.error_rate 必须在 (0,1] 之间", where)
		}
	}
	if rt != nil {
		if rt.MaxAttempts < 1 {
			errs.add("%s 的 retry.max_attempts 至少为 1", where)
		}
		if rt.Backoff < 0 || rt.MaxBackoff < 0 {
			errs.add("%s 的 retry backoff 不能为负", where)
		}
	}
}

// validateListenAddr 校验 host:port 形式。
func validateListenAddr(field, addr string) error {
	if addr == "" {
		return fmt.Errorf("%s 不能为空", field)
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("%s %q 不是合法的 host:port: %w", field, addr, err)
	}
	if port == "" {
		return fmt.Errorf("%s %q 缺少端口", field, addr)
	}
	if p, err := strconv.Atoi(port); err != nil || p < 0 || p > 65535 {
		return fmt.Errorf("%s %q 的端口非法", field, addr)
	}
	if host != "" && net.ParseIP(host) == nil && !validHostname(host) {
		return fmt.Errorf("%s %q 的 host 非法", field, addr)
	}
	return nil
}

// listenConflict 判断两个监听地址是否会抢占同一端口。
//
// 端口相同且任一方绑定通配地址（空 / 0.0.0.0 / ::）即视为冲突；
// 端口为 0 时由内核分配，不构成冲突。
func listenConflict(a, b string) bool {
	ah, ap, err1 := net.SplitHostPort(a)
	bh, bp, err2 := net.SplitHostPort(b)
	if err1 != nil || err2 != nil || ap != bp || ap == "0" {
		return false
	}
	if isWildcardHost(ah) || isWildcardHost(bh) {
		return true
	}
	return strings.EqualFold(ah, bh)
}

func isWildcardHost(h string) bool {
	switch h {
	case "", "0.0.0.0", "::", "[::]":
		return true
	}
	return false
}

func validHostname(h string) bool {
	if len(h) > 253 {
		return false
	}
	for _, r := range h {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-' || r == '.' || r == '_':
		default:
			return false
		}
	}
	return true
}
