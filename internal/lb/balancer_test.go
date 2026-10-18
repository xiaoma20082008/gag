package lb

import (
	"context"
	"testing"

	"gag/internal/core"
)

func node(addr string, weight int) *core.Instance {
	return &core.Instance{ID: addr, Address: addr, Weight: weight}
}

func nodes(spec ...any) []*core.Instance {
	out := make([]*core.Instance, 0, len(spec)/2)
	for i := 0; i+1 < len(spec); i += 2 {
		out = append(out, node(spec[i].(string), spec[i+1].(int)))
	}
	return out
}

func TestNewRejectsUnknownKind(t *testing.T) {
	if _, err := New("magic"); err == nil {
		t.Fatal("未知策略应报错")
	}
	for _, kind := range []string{"", "round_robin", "random", "least_conn", "consistent_hash"} {
		b, err := New(kind)
		if err != nil {
			t.Fatalf("New(%q): %v", kind, err)
		}
		if b.Name() == "" {
			t.Fatalf("New(%q) 没有名字", kind)
		}
	}
}

func TestSelectWithoutNodes(t *testing.T) {
	for _, kind := range []string{"round_robin", "random", "least_conn", "consistent_hash"} {
		b, err := New(kind)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := b.Select(context.Background(), nil); err == nil {
			t.Fatalf("%s：空实例列表应报错", kind)
		}
	}
}

func TestRoundRobinHonorsWeights(t *testing.T) {
	b, _ := New("round_robin")
	got := []*core.Instance{
		node("a", 1), node("b", 2), node("c", 1),
	}

	counts := map[string]int{}
	for i := 0; i < 400; i++ {
		n, err := b.Select(context.Background(), got)
		if err != nil {
			t.Fatal(err)
		}
		counts[n.Address]++
	}
	// 权重 1:2:1，期望 100:200:100。
	for addr, want := range map[string]int{"a": 100, "b": 200, "c": 100} {
		if counts[addr] != want {
			t.Fatalf("%s 被选中 %d 次, want %d", addr, counts[addr], want)
		}
	}
}

func TestRoundRobinSpreadsEvenlyWithoutWeights(t *testing.T) {
	b, _ := New("round_robin")
	got := nodes("a", 0, "b", 0, "c", 0) // 未配权重按 1 计
	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		n, _ := b.Select(context.Background(), got)
		seen[n.Address] = true
	}
	if len(seen) != 3 {
		t.Fatalf("前 3 次应覆盖全部实例，实际 %v", seen)
	}
}

func TestRandomRespectsWeights(t *testing.T) {
	b, _ := New("random")
	got := nodes("a", 1, "b", 3)

	counts := map[string]int{}
	for i := 0; i < 2000; i++ {
		n, err := b.Select(context.Background(), got)
		if err != nil {
			t.Fatal(err)
		}
		counts[n.Address]++
	}
	if counts["a"] == 0 || counts["b"] == 0 {
		t.Fatalf("两个实例都应被选中: %v", counts)
	}
	// 权重 1:3，b 应该明显更多；留足随机余量。
	if counts["b"] <= counts["a"] {
		t.Fatalf("权重更高的实例应被选得更多: %v", counts)
	}
}

// TestLeastConnTracksInflight 验证 least_conn 会挑在途最少的实例，
// 并且在 ctx 结束后归还计数（接口没有 Release，归还是通过 ctx 实现的）。
func TestLeastConnTracksInflight(t *testing.T) {
	b, _ := New("least_conn")
	got := nodes("a", 1, "b", 1)

	// 第一个请求落到 a（都在途 0，取第一个）。
	ctxA, cancelA := context.WithCancel(context.Background())
	first, _ := b.Select(ctxA, got)
	if first.Address != "a" {
		t.Fatalf("首次应选中 a，实际 %s", first.Address)
	}

	// 第二个请求应避开 a。
	ctxB, cancelB := context.WithCancel(context.Background())
	second, _ := b.Select(ctxB, got)
	if second.Address != "b" {
		t.Fatalf("a 在途时应选中 b，实际 %s", second.Address)
	}

	cancelA()
	cancelB()

	// 计数归还后，两个实例重新等位。
	ctxC, cancelC := context.WithCancel(context.Background())
	defer cancelC()
	third, _ := b.Select(ctxC, got)
	if third.Address != "a" {
		t.Fatalf("计数归还后应重新从 a 开始，实际 %s", third.Address)
	}
}

// TestLeastConnPrefersHigherWeight 验证在途计数按权重归一：
// 权重 5 的实例可以承载 5 倍于权重 1 实例的在途请求。
func TestLeastConnPrefersHigherWeight(t *testing.T) {
	b, _ := New("least_conn")
	got := nodes("a", 1, "b", 5)

	// ctx 一直不取消，计数持续累加，模拟并发在途。
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	counts := map[string]int{}
	for i := 0; i < 600; i++ {
		n, err := b.Select(ctx, got)
		if err != nil {
			t.Fatal(err)
		}
		counts[n.Address]++
	}
	if counts["a"] == 0 || counts["b"] == 0 {
		t.Fatalf("两个实例都应该被选中: %v", counts)
	}
	// 权重 1:5，b 的被选次数应明显更多。
	if counts["b"] < counts["a"]*2 {
		t.Fatalf("权重更高的实例应被选得更多: %v", counts)
	}
}

func TestConsistentHashIsStable(t *testing.T) {
	b, _ := New("consistent_hash")
	got := nodes("a", 1, "b", 1, "c", 1)

	ctx := WithHashKey(context.Background(), "user-42")
	first, _ := b.Select(ctx, got)
	for i := 0; i < 50; i++ {
		n, _ := b.Select(ctx, got)
		if n.Address != first.Address {
			t.Fatalf("同一 key 应稳定落到同一实例：%s vs %s", first.Address, n.Address)
		}
	}

	// 不同 key 应该分散到多个实例上。
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		n, _ := b.Select(WithHashKey(context.Background(), "user-"+string(rune('A'+i%26))), got)
		seen[n.Address] = true
	}
	if len(seen) < 2 {
		t.Fatalf("不同 key 应分散，实际只落到 %v", seen)
	}
}

// TestConsistentHashKeepsMostKeysWhenNodeChanges 是一致性的核心保证：
// 摘掉一个实例后，原来落在其它实例上的 key 不应改道。
func TestConsistentHashKeepsMostKeysWhenNodeChanges(t *testing.T) {
	before := nodes("a", 1, "b", 1, "c", 1)
	after := nodes("a", 1, "b", 1)
	b, _ := New("consistent_hash")

	keys := make([]string, 200)
	for i := range keys {
		keys[i] = "user-" + string(rune(i))
	}
	stay := 0
	for _, k := range keys {
		x, _ := b.Select(WithHashKey(context.Background(), k), before)
		y, _ := b.Select(WithHashKey(context.Background(), k), after)
		if x.Address == y.Address {
			stay++
		}
	}
	// 摘掉 1/3 的节点，理想情况下约 2/3 的 key 不变。
	if stay < len(keys)/2 {
		t.Fatalf("摘节点后只有 %d/%d 的 key 保持原实例，一致性不足", stay, len(keys))
	}
}

// TestConsistentHashFallsBackWithoutKey 保证没有稳定标识时不会
// 把所有匿名流量打到同一个实例。
func TestConsistentHashFallsBackWithoutKey(t *testing.T) {
	b, _ := New("consistent_hash")
	got := nodes("a", 1, "b", 1, "c", 1)

	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		n, err := b.Select(context.Background(), got)
		if err != nil {
			t.Fatal(err)
		}
		seen[n.Address] = true
	}
	if len(seen) < 2 {
		t.Fatalf("无 key 时应退化为随机分布，实际只落到 %v", seen)
	}
}

// TestSelectIgnoresNodeOrderForHash 确认 HRW 不依赖节点排列顺序。
func TestSelectIgnoresNodeOrderForHash(t *testing.T) {
	b, _ := New("consistent_hash")
	forward := nodes("a", 1, "b", 1, "c", 1)
	reversed := nodes("c", 1, "b", 1, "a", 1)

	for i := 0; i < 50; i++ {
		key := "user-" + string(rune('a'+i%26))
		x, _ := b.Select(WithHashKey(context.Background(), key), forward)
		y, _ := b.Select(WithHashKey(context.Background(), key), reversed)
		if x.Address != y.Address {
			t.Fatalf("key %q 因节点顺序改变而改道: %s vs %s", key, x.Address, y.Address)
		}
	}
}
