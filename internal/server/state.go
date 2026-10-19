package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gag/internal/config"
	"gag/internal/discovery"
	"gag/internal/middleware"
	"gag/internal/router"
)

// State 是数据平面的运行时视图，实现 router.State。
//
// 一次配置对应一个 State，构建完成后只读；配置变更时整体重建并原子替换，
// 因此请求路径上没有锁，也不存在"读到半份配置"的窗口。
type State struct {
	snapshot *config.Snapshot
	table    *router.Table
	policies map[string]*middleware.Set
	// source 是实例来源，装配治理链时交给 discovery.Resolver。
	source discovery.DiscoveryClient

	// reused 记录本次重建复用了多少个策略集合，用于启动/热加载日志。
	reused int
}

// NewState 由快照构建运行时视图。
//
// prev 非空时，哈希一致的路由会复用旧 middleware.Set：
// 限流令牌桶与熔断计数因此得以跨配置发布延续，
// 避免每次发布都把在途流量当成新一轮冷启动。
func NewState(snap *config.Snapshot, prev *State) (*State, error) {
	if snap == nil || snap.Config() == nil {
		return nil, errors.New("server: snapshot 为 nil")
	}

	// 实例来源由配置的 discovery 段决定：静态场景用配置写死的清单，
	// nacos 走注册中心查询（客户端按连接参数进程内复用，不会随发布重建）。
	// 连接参数不合法时在这里失败——宁可拒绝发布，也不要静默跑在一个
	// 永远没有实例的网关上，那只会让所有请求先超时才 503。
	src, err := discovery.NewDiscoveryClient(snap.Config().Server.Discovery)
	if err != nil {
		return nil, fmt.Errorf("server: 实例来源: %w", err)
	}
	table, err := router.NewTable(snap)
	if err != nil {
		return nil, fmt.Errorf("构建路由表: %w", err)
	}

	cfg := snap.Config()
	st := &State{
		snapshot: snap,
		table:    table,
		source:   src,
		policies: make(map[string]*middleware.Set, table.RouteCount()),
	}

	for ti := range cfg.Tenants {
		tenant := &cfg.Tenants[ti]
		for si := range tenant.Services {
			svc := &tenant.Services[si]
			for ri := range svc.Routes {
				route := &svc.Routes[ri]
				set, err := middleware.BuildSet(route.ID, svc, route)
				if err != nil {
					return nil, fmt.Errorf("构建策略 %s: %w", route.ID, err)
				}
				if prev != nil {
					if old := prev.Policy(route.ID); old != nil && old.Hash == set.Hash {
						set = old
						st.reused++
					}
				}
				st.policies[route.ID] = set
			}
		}
	}

	warmDiscovery(src, cfg)
	return st, nil
}

// warmDiscovery 提前查询一遍注册中心，把实例缓存焐热。
//
// 只对注册中心来源有意义（静态来源查的是内存，没有冷启动问题）。
// 查询放在后台 goroutine 里串行做：装配期不能因为等注册中心而阻塞，
// 也没有必要为几十个集群开几十个并发把注册中心打满。
// 失败的查询会被 SDK 自己的刷新循环兜底重试，这里不处理错误。
func warmDiscovery(src discovery.DiscoveryClient, cfg *config.Config) {
	w, ok := src.(discovery.Warmer)
	if !ok {
		return
	}
	go func() {
		for ti := range cfg.Tenants {
			tenant := &cfg.Tenants[ti]
			for si := range tenant.Services {
				svc := &tenant.Services[si]
				for ci := range svc.Clusters {
					cl := &svc.Clusters[ci]
					ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
					_ = w.Warm(ctx, tenant.Name, svc.Name, cl.Name)
					cancel()
				}
			}
		}
	}()
}

// Snapshot 返回当前生效的配置快照。
func (s *State) Snapshot() *config.Snapshot { return s.snapshot }

// Table 返回编译后的路由索引。
func (s *State) Table() *router.Table { return s.table }

// Policy 返回路由对应的治理策略集合。
func (s *State) Policy(routeID string) *middleware.Set { return s.policies[routeID] }

// Source 返回实例来源，装配治理链时交给 discovery.Resolver。
func (s *State) Source() discovery.DiscoveryClient { return s.source }

// RouteCount 返回已编译的路由条数。
func (s *State) RouteCount() int { return len(s.policies) }

// TenantCount 返回租户数量。
func (s *State) TenantCount() int { return len(s.snapshot.Config().Tenants) }

// ReusedPolicies 返回本次重建复用的策略数量。
func (s *State) ReusedPolicies() int { return s.reused }
