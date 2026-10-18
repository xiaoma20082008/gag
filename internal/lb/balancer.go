// Package lb 提供集群内的负载均衡策略。
//
// Balancer 只负责"在一组实例里挑一个"：实例列表由调用方每次传入
// （来自配置或注册中心），balancer 自己不持有节点，因此实例扩缩容、
// 摘除、发布都不需要重建 balancer，也不会丢掉 least_conn 的在途计数。
//
// 一致性哈希需要一个"按谁稳定"的键（登录用户、客户端 IP ...），
// 而 Select 的签名里没有独立参数，因此由调用方通过 WithHashKey 放在
// context 上传递。
package lb

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand/v2"
	"strconv"
	"sync"
	"sync/atomic"

	"gag/internal/core"
)

var ErrNoNodes = errors.New("lb: no available instances")

type LoadBalancer interface {
	Name() string
	Select(ctx context.Context, nodes []*core.Instance) (*core.Instance, error)
}

func New(kind string) (LoadBalancer, error) {
	switch kind {
	case "", "round_robin":
		return &roundRobin{}, nil
	case "random":
		return &randomBalancer{}, nil
	case "least_conn":
		return &leastConn{}, nil
	case "consistent_hash":
		return &consistentHash{}, nil
	default:
		return nil, fmt.Errorf("lb: 未知的负载均衡策略 %q", kind)
	}
}

// ---------- 一致性哈希键 ----------

type hashKey struct{}

func WithHashKey(ctx context.Context, key string) context.Context {
	return context.WithValue(ctx, hashKey{}, key)
}

func hashKeyFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	key, _ := ctx.Value(hashKey{}).(string)
	return key
}

// ---------- round robin ----------

type roundRobin struct {
	// counter 是原子自增的"票号"：对总权重取模后按累计权重映射到实例，
	// 权重为 w 的实例因此占到 w 个位置，被选中的次数与权重成正比。
	counter atomic.Uint64
}

func (b *roundRobin) Name() string { return "round_robin" }

func (b *roundRobin) Select(ctx context.Context, nodes []*core.Instance) (*core.Instance, error) {
	if len(nodes) == 0 {
		return nil, ErrNoNodes
	}
	cum, total := weights(nodes)

	// 票号对总权重取模：权重为 w 的实例占 w 个位置，因此被选中的次数
	// 与权重成正比。取模而不展开成环，避免每次调用都重新分配切片。
	ticket := b.counter.Add(1) - 1

	return nodes[pickByWeight(cum, total, int(ticket%uint64(total)))], nil
}

// ---------- random ----------

type randomBalancer struct {
}

func (b *randomBalancer) Name() string { return "random" }

func (b *randomBalancer) Select(_ context.Context, nodes []*core.Instance) (*core.Instance, error) {
	if len(nodes) == 0 {
		return nil, ErrNoNodes
	}
	cum, total := weights(nodes)
	return nodes[pickByWeight(cum, total, rand.IntN(total))], nil
}

// ---------- least connections ----------

type leastConn struct {
	mu       sync.Mutex
	inflight map[string]int64
}

func (b *leastConn) Name() string { return "least_conn" }

func (b *leastConn) Select(ctx context.Context, nodes []*core.Instance) (*core.Instance, error) {
	if len(nodes) == 0 {
		return nil, ErrNoNodes
	}

	b.mu.Lock()
	if b.inflight == nil {
		b.inflight = map[string]int64{}
	}
	best, bestScore := 0, math.MaxFloat64
	for i, n := range nodes {
		// 在途数除以权重：权重越高的实例允许承载更多在途请求。
		score := float64(b.inflight[instanceKey(n)]) / float64(weightOf(n))
		if score < bestScore {
			best, bestScore = i, score
		}
	}
	key := instanceKey(nodes[best])
	b.inflight[key]++
	b.mu.Unlock()

	// 接口里没有 Release：在途计数随 context 结束归还。
	// AfterFunc 比常驻 goroutine 便宜，ctx 不可取消时（Background）直接跳过。
	if ctx != nil && ctx.Done() != nil {
		context.AfterFunc(ctx, func() {
			b.mu.Lock()
			if n := b.inflight[key] - 1; n > 0 {
				b.inflight[key] = n
			} else {
				delete(b.inflight, key)
			}
			b.mu.Unlock()
		})
	}
	return nodes[best], nil
}

// ---------- consistent hash ----------

type consistentHash struct {
}

const virtualNodes = 64

func (b *consistentHash) Name() string { return "consistent_hash" }

// Select 用 rendezvous 哈希（HRW）选实例。
func (b *consistentHash) Select(ctx context.Context, nodes []*core.Instance) (*core.Instance, error) {
	if len(nodes) == 0 {
		return nil, ErrNoNodes
	}
	key := hashKeyFrom(ctx)
	if key == "" {
		// 没有稳定标识时退化为随机，避免所有匿名请求都打到同一个实例。
		return nodes[rand.IntN(len(nodes))], nil
	}

	best := 0
	var bestScore uint64
	first := true
	for i, n := range nodes {
		for v := 0; v < virtualNodes*weightOf(n); v++ {
			h := hashString(key + "#" + instanceKey(n) + "#" + strconv.Itoa(v))
			if first || h > bestScore {
				best, bestScore, first = i, h, false
			}
		}
	}
	return nodes[best], nil
}

// ---------- 公共工具 ----------

// weights 返回累计权重与总权重；未配权重的实例按 1 计。
func weights(nodes []*core.Instance) (cum []int, total int) {
	cum = make([]int, len(nodes))
	for i, n := range nodes {
		total += weightOf(n)
		cum[i] = total
	}
	return cum, total
}

func pickByWeight(cum []int, total, point int) int {
	for i, c := range cum {
		if point < c {
			return i
		}
	}
	return len(cum) - 1
}

func weightOf(n *core.Instance) int {
	if n == nil || n.Weight <= 0 {
		return 1
	}
	return n.Weight
}

// instanceKey 用在途计数的键：地址在集群内唯一。
func instanceKey(n *core.Instance) string {
	if n == nil {
		return ""
	}
	if n.Address != "" {
		return n.Address
	}
	return n.ID
}

func hashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}
