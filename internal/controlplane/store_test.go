package controlplane

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"gag/internal/config"
)

func mustConfig(t *testing.T, version string) *config.Config {
	t.Helper()
	cfg, err := config.Load(strings.NewReader(`
version: ` + version + `
server:
  listen: ":0"
  default_tenant: t
tenants:
  - name: t
    services:
      - name: s
        routes:
          - id: r
            path: /x
            cluster: c
        clusters:
          - name: c
            instances:
              - addr: 127.0.0.1:1
`))
	if err != nil {
		t.Fatalf("构造配置: %v", err)
	}
	return cfg
}

func TestPublishAndHistory(t *testing.T) {
	s := NewStore()
	v1, err := s.Publish(mustConfig(t, "v1"), "alice", "first")
	if err != nil {
		t.Fatal(err)
	}
	if v1.ID != 1 || s.Current().Version != "v1" {
		t.Fatalf("首次发布异常: %+v", v1)
	}
	v2, err := s.Publish(mustConfig(t, "v2"), "bob", "second")
	if err != nil {
		t.Fatal(err)
	}
	if v2.ID != 2 || s.Current().Version != "v2" {
		t.Fatalf("第二次发布异常: %+v", v2)
	}
	if got := len(s.History()); got != 2 {
		t.Fatalf("历史版本数 = %d", got)
	}
}

func TestRollbackCreatesNewVersion(t *testing.T) {
	s := NewStore()
	v1, _ := s.Publish(mustConfig(t, "v1"), "alice", "first")
	if _, err := s.Publish(mustConfig(t, "v2"), "bob", "second"); err != nil {
		t.Fatal(err)
	}

	rb, err := s.Rollback(v1.ID, "ops")
	if err != nil {
		t.Fatal(err)
	}
	if rb.ID != 3 {
		t.Fatalf("回滚应生成新版本 #3，实际 #%d", rb.ID)
	}
	if s.Current().Version != "v1" {
		t.Fatalf("回滚后当前版本 = %q", s.Current().Version)
	}
	if !strings.Contains(rb.Comment, "rollback") {
		t.Fatalf("回滚记录缺少说明: %q", rb.Comment)
	}
	if len(s.History()) != 3 {
		t.Fatalf("回滚应保留审计历史，实际 %d 条", len(s.History()))
	}

	if _, err := s.Rollback(99, "ops"); !errors.Is(err, ErrVersionNotFound) {
		t.Fatalf("不存在的版本应报错，实际 %v", err)
	}
}

func TestWatchReceivesUpdates(t *testing.T) {
	s := NewStore()
	if _, err := s.Publish(mustConfig(t, "v1"), "alice", ""); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch := s.Watch(ctx)

	select {
	case snap := <-ch:
		if snap.Version() != "v1" {
			t.Fatalf("首次订阅应收到当前版本，实际 %q", snap.Version())
		}
	case <-time.After(time.Second):
		t.Fatal("订阅未收到当前快照")
	}

	if _, err := s.Publish(mustConfig(t, "v2"), "bob", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case snap := <-ch:
		if snap.Version() != "v2" {
			t.Fatalf("变更后应收到 v2，实际 %q", snap.Version())
		}
	case <-time.After(time.Second):
		t.Fatal("订阅未收到变更")
	}
}

func TestPublishRejectsInvalidConfig(t *testing.T) {
	s := NewStore()
	cfg := mustConfig(t, "v1")
	cfg.Tenants[0].Services[0].Routes[0].Cluster = "missing"
	if _, err := s.Publish(cfg, "alice", ""); err == nil {
		t.Fatal("非法配置应被拒绝")
	}
	if s.Current() != nil {
		t.Fatal("发布失败不应改变当前版本")
	}
}
