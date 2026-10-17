package routing

import (
	"hash/fnv"
	"math/rand/v2"
	"strconv"
	"strings"

	"gag/internal/config"
)

// selectCluster 执行灰度分流，返回目标集群名。
//
// 规则优先级：带条件的规则按声明顺序先匹配，命中即返回；
// 之后在无条件的规则之间按权重做稳定分配，保证同一调用方始终落到同一集群。
func selectCluster(route *config.Route, meta *RequestMeta) string {
	if len(route.Split) == 0 {
		return route.Cluster
	}

	for _, rule := range route.Split {
		if rule.Match == nil {
			continue
		}
		if matchSplit(rule.Match, route.ID, meta) {
			return rule.Cluster
		}
	}

	total := 0
	for _, rule := range route.Split {
		if rule.Match == nil && rule.Weight > 0 {
			total += rule.Weight
		}
	}
	if total == 0 {
		return route.Cluster
	}

	key := splitKey(route.ID, meta)
	var point int
	if key == "" {
		// 没有稳定标识时退化为随机，避免所有匿名请求都打到同一个集群。
		point = rand.IntN(total)
	} else {
		point = int(hash32(key) % uint32(total))
	}
	acc := 0
	for _, rule := range route.Split {
		if rule.Match != nil || rule.Weight <= 0 {
			continue
		}
		acc += rule.Weight
		if point < acc {
			return rule.Cluster
		}
	}
	return route.Cluster
}

func matchSplit(m *config.SplitMatch, routeID string, meta *RequestMeta) bool {
	for k, want := range m.Headers {
		if meta.Header.Get(k) != want {
			return false
		}
	}
	if len(m.Cookies) > 0 {
		cookies := parseCookies(meta.Header)
		for k, want := range m.Cookies {
			if cookies[k] != want {
				return false
			}
		}
	}
	if len(m.Users) > 0 && !contains(m.Users, meta.User) {
		return false
	}
	if len(m.Regions) > 0 && !contains(m.Regions, meta.Region) {
		return false
	}
	if m.Percentage > 0 {
		key := splitKey(routeID, meta)
		if key == "" {
			return false
		}
		if float64(hash32(key)%10000)/100.0 >= m.Percentage {
			return false
		}
	}
	return true
}

// splitKey 选择用于稳定分流的标识：优先登录用户，其次客户端 IP。
func splitKey(routeID string, meta *RequestMeta) string {
	identity := meta.User
	if identity == "" {
		identity = meta.ClientIP
	}
	if identity == "" {
		return ""
	}
	return routeID + "|" + identity
}

func parseCookies(h map[string][]string) map[string]string {
	out := make(map[string]string, 4)
	for _, line := range h["Cookie"] {
		for _, part := range strings.Split(line, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			k, v, ok := strings.Cut(part, "=")
			if !ok {
				continue
			}
			out[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	return out
}

func contains(list []string, want string) bool {
	if want == "" {
		return false
	}
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

func hash32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// WeightedCluster 便于控制面展示当前权重分布。
type WeightedCluster struct {
	Cluster string
	Weight  int
	Percent float64
}

// Distribution 计算某条路由的权重分布，供管理接口展示灰度比例。
func Distribution(route *config.Route) []WeightedCluster {
	total := 0
	for _, rule := range route.Split {
		if rule.Match == nil {
			total += rule.Weight
		}
	}
	out := make([]WeightedCluster, 0, len(route.Split))
	for _, rule := range route.Split {
		w := WeightedCluster{Cluster: rule.Cluster, Weight: rule.Weight}
		if rule.Match == nil && total > 0 {
			w.Percent = float64(rule.Weight) / float64(total) * 100
		}
		out = append(out, w)
	}
	return out
}

// PercentLabel 用于日志里输出百分比，保留一位小数。
func PercentLabel(v float64) string { return strconv.FormatFloat(v, 'f', 1, 64) }
