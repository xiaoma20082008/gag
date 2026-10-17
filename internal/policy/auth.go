package policy

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"gag/internal/config"
)

// 认证相关错误。
var (
	ErrUnauthenticated = errors.New("policy: unauthenticated")
	ErrForbidden       = errors.New("policy: forbidden")
)

// Principal 是通过认证后得到的调用方身份。
type Principal struct {
	Subject string
	Tenant  string
	Scopes  []string
	Method  string
}

// HasScope 判断是否具备指定 scope。
func (p *Principal) HasScope(scope string) bool {
	if p == nil {
		return false
	}
	for _, s := range p.Scopes {
		if s == scope || s == "*" {
			return true
		}
	}
	return false
}

// Authenticator 校验入站请求身份。
type Authenticator interface {
	Authenticate(r *http.Request) (*Principal, error)
	Type() string
}

// NewAuthenticator 根据配置创建认证器；type=none 时返回 nil。
func NewAuthenticator(p *config.AuthPolicy) (Authenticator, error) {
	if p == nil || p.Type == "" || p.Type == "none" {
		return nil, nil
	}
	switch p.Type {
	case "api_key":
		if len(p.Keys) == 0 {
			return nil, errors.New("policy: api_key 认证缺少 keys")
		}
		header := p.Header
		if header == "" {
			header = "X-API-Key"
		}
		keys := make(map[string]struct{}, len(p.Keys))
		for _, k := range p.Keys {
			if k != "" {
				keys[k] = struct{}{}
			}
		}
		return &apiKeyAuth{header: header, keys: keys}, nil
	case "jwt":
		if p.Secret == "" {
			return nil, errors.New("policy: jwt 认证缺少 secret")
		}
		header := p.Header
		if header == "" {
			header = "Authorization"
		}
		claim := p.TenantClaim
		if claim == "" {
			claim = "tenant"
		}
		return &jwtAuth{
			header:   header,
			secret:   []byte(p.Secret),
			issuer:   p.Issuer,
			audience: p.Audience,
			tenant:   claim,
			scopes:   p.Scopes,
		}, nil
	default:
		return nil, fmt.Errorf("policy: 未知认证类型 %q", p.Type)
	}
}

// ---------- API Key ----------

type apiKeyAuth struct {
	header string
	keys   map[string]struct{}
}

func (a *apiKeyAuth) Type() string { return "api_key" }

func (a *apiKeyAuth) Authenticate(r *http.Request) (*Principal, error) {
	key := strings.TrimSpace(r.Header.Get(a.header))
	if key == "" {
		return nil, ErrUnauthenticated
	}
	for candidate := range a.keys {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(key)) == 1 {
			return &Principal{
				// 不把密钥本身写进日志或 trace，只保留可关联的短指纹。
				Subject: "api_key:" + fingerprint(key),
				Method:  "api_key",
			}, nil
		}
	}
	return nil, ErrUnauthenticated
}

func fingerprint(key string) string {
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("%x", sum[:4])
}

// ---------- JWT (HS256) ----------

type jwtAuth struct {
	header   string
	secret   []byte
	issuer   string
	audience string
	tenant   string
	scopes   []string
}

func (a *jwtAuth) Type() string { return "jwt" }

func (a *jwtAuth) Authenticate(r *http.Request) (*Principal, error) {
	raw := strings.TrimSpace(r.Header.Get(a.header))
	raw = strings.TrimPrefix(raw, "Bearer ")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrUnauthenticated
	}

	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return nil, ErrUnauthenticated
	}
	headJSON, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, ErrUnauthenticated
	}
	var head struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(headJSON, &head); err != nil || head.Alg != "HS256" {
		return nil, ErrUnauthenticated
	}

	signingInput := parts[0] + "." + parts[1]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, ErrUnauthenticated
	}
	mac := hmac.New(sha256.New, a.secret)
	_, _ = mac.Write([]byte(signingInput))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, ErrUnauthenticated
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrUnauthenticated
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, ErrUnauthenticated
	}
	now := time.Now().Unix()
	if exp, ok := numericClaim(claims, "exp"); ok && now >= exp {
		return nil, ErrUnauthenticated
	}
	if nbf, ok := numericClaim(claims, "nbf"); ok && now < nbf {
		return nil, ErrUnauthenticated
	}
	if a.issuer != "" {
		if iss, _ := claims["iss"].(string); iss != a.issuer {
			return nil, ErrUnauthenticated
		}
	}
	if a.audience != "" && !audienceMatches(claims["aud"], a.audience) {
		return nil, ErrUnauthenticated
	}

	sub, _ := claims["sub"].(string)
	tenant, _ := claims[a.tenant].(string)
	scopes := a.scopes
	if raw, ok := claims["scope"].(string); ok && raw != "" {
		scopes = strings.Fields(raw)
	} else if list, ok := claims["scopes"].([]any); ok {
		scopes = make([]string, 0, len(list))
		for _, item := range list {
			if s, ok := item.(string); ok {
				scopes = append(scopes, s)
			}
		}
	}
	return &Principal{Subject: sub, Tenant: tenant, Scopes: scopes, Method: "jwt"}, nil
}

func numericClaim(claims map[string]any, key string) (int64, bool) {
	v, ok := claims[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	}
	return 0, false
}

func audienceMatches(claim any, want string) bool {
	switch v := claim.(type) {
	case string:
		return v == want
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s == want {
				return true
			}
		}
	}
	return false
}
