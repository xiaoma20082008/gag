// Package lb 提供集群内的负载均衡策略。
//
// Balancer 只负责"在一组节点里挑一个"，健康判断通过 healthy 回调注入，
// 这样健康状态变化不需要重建 balancer，也不会丢 least_conn 的在途计数。
package lb

import (
	"errors"
	"fmt"
	"hash/fnv"
	"math/rand/v2"
	"sort"
	"sync"
	"sync/atomic"
)

// ErrNoNodes 表示没有可用节点。
var ErrNoNodes = errors.New("lb: no available nodes")

// Node 是负载均衡的最小单位。
type Node struct {
	Addr   string
	Weight int
	Meta   map[string]string
}

// Balancer 是负载均衡策略。
type Balancer interface {
	// Pick 按策略选出一个节点；healthy 为 nil 时视为全部健康。
	Pick(key string, healthy func(*Node) bool) (*Node, error)
	// Release 归还节点，目前仅 least_conn 使用。
	Release(n *Node)
	Kind() string
	Size() int
}

// New 按类型构造 balancer。
func New(kind string, nodes []Node) (Balancer, error) {
	switch kind {
	case "", "round_robin":
		return newRoundRobin(nodes), nil
	case "random":
		return newRandom(nodes), nil
	case "least_conn":
		return newLeastConn(nodes), nil
	case "consistent_hash":
		return newHashRing(nodes), nil
	default:
		return nil, fmt.Errorf("lb: 未知的负载均衡策略 %q", kind)
	}
}

func normalize(nodes []Node) []Node {
	out := make([]Node, len(nodes))
	copy(out, nodes)
	for i := range out {
		if out[i].Weight <= 0 {
			out[i].Weight = 1
		}
	}
	return out
}

// ---------- round robin ----------

type roundRobin struct {
	ring    []*Node
	counter atomic.Uint64
}

func newRoundRobin(nodes []Node) *roundRobin {
	rb := &roundRobin{}
	for i := range nodes {
		w := nodes[i].Weight
		if w <= 0 {
			w = 1
		}
		n := nodes[i]
		for j := 0; j < w; j++ {
			rb.ring = append(rb.ring, &n)
		}
	}
	return rb
}

func (b *roundRobin) Kind() string { return "round_robin" }
func (b *roundRobin) Size() int    { return len(b.ring) }

func (b *roundRobin) Pick(_ string, healthy func(*Node) bool) (*Node, error) {
	if len(b.ring) == 0 {
		return nil, ErrNoNodes
	}
	start := int(b.counter.Add(1) % uint64(len(b.ring)))
	for i := 0; i < len(b.ring); i++ {
		n := b.ring[(start+i)%len(b.ring)]
		if healthy == nil || healthy(n) {
			return n, nil
		}
	}
	return nil, ErrNoNodes
}

func (b *roundRobin) Release(*Node) {}

// ---------- random ----------

type randomBalancer struct {
	nodes  []Node
	total  int
	prefix []int
}

func newRandom(nodes []Node) *randomBalancer {
	b := &randomBalancer{nodes: normalize(nodes)}
	b.prefix = make([]int, len(b.nodes))
	sum := 0
	for i := range b.nodes {
		sum += b.nodes[i].Weight
		b.prefix[i] = sum
	}
	b.total = sum
	return b
}

func (b *randomBalancer) Kind() string { return "random" }
func (b *randomBalancer) Size() int    { return len(b.nodes) }

func (b *randomBalancer) Pick(_ string, healthy func(*Node) bool) (*Node, error) {
	if len(b.nodes) == 0 {
		return nil, ErrNoNodes
	}
	if healthy == nil {
		return b.pickRandom()
	}
	// 先随机尝试若干次，避免高负载时每次都全量扫描。
	for i := 0; i < 4; i++ {
		n, err := b.pickRandom()
		if err != nil {
			return nil, err
		}
		if healthy(n) {
			return n, nil
		}
	}
	for i := range b.nodes {
		if healthy(&b.nodes[i]) {
			return &b.nodes[i], nil
		}
	}
	return nil, ErrNoNodes
}

func (b *randomBalancer) pickRandom() (*Node, error) {
	if b.total <= 0 {
		return nil, ErrNoNodes
	}
	r := rand.IntN(b.total)
	idx := sort.SearchInts(b.prefix, r+1)
	if idx >= len(b.nodes) {
		idx = len(b.nodes) - 1
	}
	return &b.nodes[idx], nil
}

func (b *randomBalancer) Release(*Node) {}

// ---------- least connections ----------

type leastConn struct {
	nodes    []Node
	inflight []atomic.Int64
}

func newLeastConn(nodes []Node) *leastConn {
	return &leastConn{nodes: normalize(nodes), inflight: make([]atomic.Int64, len(nodes))}
}

func (b *leastConn) Kind() string { return "least_conn" }
func (b *leastConn) Size() int    { return len(b.nodes) }

func (b *leastConn) Pick(_ string, healthy func(*Node) bool) (*Node, error) {
	best := -1
	var bestScore float64
	for i := range b.nodes {
		if healthy != nil && !healthy(&b.nodes[i]) {
			continue
		}
		// 用 在途数/权重 做比较，权重越高越容易被选中。
		w := b.nodes[i].Weight
		if w <= 0 {
			w = 1
		}
		score := float64(b.inflight[i].Load()) / float64(w)
		if best == -1 || score < bestScore {
			best, bestScore = i, score
		}
	}
	if best == -1 {
		return nil, ErrNoNodes
	}
	b.inflight[best].Add(1)
	return &b.nodes[best], nil
}

func (b *leastConn) Release(n *Node) {
	if n == nil {
		return
	}
	for i := range b.nodes {
		if b.nodes[i].Addr == n.Addr {
			if b.inflight[i].Add(-1) < 0 {
				b.inflight[i].Store(0)
			}
			return
		}
	}
}

// ---------- consistent hash ----------

type hashRing struct {
	nodes   []Node
	hashes  []uint64
	mapping map[uint64]int
	mu      sync.RWMutex
}

const virtualNodes = 64

func newHashRing(nodes []Node) *hashRing {
	ring := &hashRing{nodes: normalize(nodes), mapping: make(map[uint64]int, len(nodes)*virtualNodes)}
	for i := range ring.nodes {
		w := ring.nodes[i].Weight
		if w <= 0 {
			w = 1
		}
		for v := 0; v < virtualNodes*w; v++ {
			h := hashString(fmt.Sprintf("%s#%d", ring.nodes[i].Addr, v))
			ring.hashes = append(ring.hashes, h)
			ring.mapping[h] = i
		}
	}
	sort.Slice(ring.hashes, func(i, j int) bool { return ring.hashes[i] < ring.hashes[j] })
	return ring
}

func (b *hashRing) Kind() string { return "consistent_hash" }
func (b *hashRing) Size() int    { return len(b.nodes) }

func (b *hashRing) Pick(key string, healthy func(*Node) bool) (*Node, error) {
	if len(b.hashes) == 0 {
		return nil, ErrNoNodes
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	h := hashString(key)
	idx := sort.Search(len(b.hashes), func(i int) bool { return b.hashes[i] >= h })
	for i := 0; i < len(b.hashes); i++ {
		pos := (idx + i) % len(b.hashes)
		n := &b.nodes[b.mapping[b.hashes[pos]]]
		if healthy == nil || healthy(n) {
			return n, nil
		}
	}
	return nil, ErrNoNodes
}

func (b *hashRing) Release(*Node) {}

func hashString(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}
