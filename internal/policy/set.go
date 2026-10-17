package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"gag/internal/config"
)

// Set 是某个路由生效的治理策略集合。
//
// 一个 Set 的生命周期跟随配置：配置未变化时数据面会复用同一个 Set，
// 从而保留限流令牌与熔断计数；配置变化则整体替换。
type Set struct {
	RouteID string
	Hash    string

	Timeout time.Duration
	Retry   RetryPolicy
	// RetryOnStatus 是额外视为可重试的下游状态码。
	RetryOnStatus []int
	RateLimit     RateLimiter
	// RateLimitKey 是限流维度：ip | tenant | route | user。
	RateLimitKey string
	Breaker      *Breaker
	Auth         Authenticator
	// RequiredScopes 是访问该路由必须携带的 scope。
	RequiredScopes []string
	Validate       *config.ValidatePolicy
	Headers        *config.HeaderPolicy
}

// Provider 提供路由对应的策略集合。
type Provider interface {
	Policy(routeID string) *Set
}

// BuildSet 合并 service 与 route 级策略，route 非空时整体覆盖。
func BuildSet(routeID string, svc *config.Service, route *config.Route) (*Set, error) {
	if route == nil {
		return nil, fmt.Errorf("policy: route 为 nil")
	}

	authCfg := firstAuth(route.Auth, svc.Auth)
	auth, err := NewAuthenticator(authCfg)
	if err != nil {
		return nil, fmt.Errorf("route %s 认证策略: %w", routeID, err)
	}

	rlCfg := firstRateLimit(route.RateLimit, svc.RateLimit)
	brCfg := firstBreaker(route.Breaker, svc.Breaker)
	rtCfg := firstRetry(route.Retry, svc.Retry)
	validate := firstValidate(route.Validate, svc.Validate)

	timeout := route.Timeout.Std()
	if timeout <= 0 {
		timeout = svc.Timeout.Std()
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	set := &Set{
		RouteID:        routeID,
		Timeout:        timeout,
		Retry:          NewRetryPolicy(rtCfg),
		RetryOnStatus:  retryOnStatus(rtCfg),
		RateLimit:      NewRateLimiter(rlCfg),
		RateLimitKey:   rateLimitKey(rlCfg),
		Breaker:        NewBreakerFromConfig(brCfg),
		Auth:           auth,
		RequiredScopes: requiredScopes(authCfg),
		Validate:       validate,
		Headers:        svc.Headers,
	}
	set.Hash = hashSet(timeout, authCfg, rlCfg, brCfg, rtCfg, validate, svc.Headers)
	return set, nil
}

func retryOnStatus(p *config.RetryPolicy) []int {
	if p == nil {
		return nil
	}
	return p.RetryOn
}

func requiredScopes(p *config.AuthPolicy) []string {
	if p == nil {
		return nil
	}
	return p.Scopes
}

func rateLimitKey(p *config.RateLimitPolicy) string {
	if p == nil || p.Key == "" {
		return "route"
	}
	return p.Key
}

func firstAuth(route, svc *config.AuthPolicy) *config.AuthPolicy {
	if route != nil {
		return route
	}
	return svc
}

func firstRateLimit(route, svc *config.RateLimitPolicy) *config.RateLimitPolicy {
	if route != nil {
		return route
	}
	return svc
}

func firstBreaker(route, svc *config.BreakerPolicy) *config.BreakerPolicy {
	if route != nil {
		return route
	}
	return svc
}

func firstRetry(route, svc *config.RetryPolicy) *config.RetryPolicy {
	if route != nil {
		return route
	}
	return svc
}

func firstValidate(route, svc *config.ValidatePolicy) *config.ValidatePolicy {
	if route != nil {
		return route
	}
	return svc
}

// hashSet 只对"配置态"字段做哈希，运行态计数不参与。
//
// 认证配置也参与哈希（含密钥摘要），这样密钥轮换会触发 Set 重建。
func hashSet(timeout time.Duration, auth *config.AuthPolicy, rl *config.RateLimitPolicy,
	br *config.BreakerPolicy, rt *config.RetryPolicy, v *config.ValidatePolicy,
	headers *config.HeaderPolicy) string {
	payload := struct {
		Timeout   time.Duration
		Auth      *config.AuthPolicy
		RateLimit *config.RateLimitPolicy
		Breaker   *config.BreakerPolicy
		Retry     *config.RetryPolicy
		Validate  *config.ValidatePolicy
		Headers   *config.HeaderPolicy
	}{timeout, auth, rl, br, rt, v, headers}

	data, _ := json.Marshal(payload)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
