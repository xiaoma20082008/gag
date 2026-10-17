package server

import (
	"errors"
	"fmt"

	"gag/internal/config"
	"gag/internal/policy"
	"gag/internal/routing"
)

// State 是数据平面的运行时视图，实现 modules.State。
//
// 一次配置对应一个 State，构建完成后只读；配置变更时整体重建并原子替换，
// 因此请求路径上没有锁，也不存在"读到半份配置"的窗口。
type State struct {
	snapshot *config.Snapshot
	table    *routing.Table
	policies map[string]*policy.Set

	// reused 记录本次重建复用了多少个策略集合，用于启动/热加载日志。
	reused int
}

// NewState 由快照构建运行时视图。
//
// prev 非空时，哈希一致的路由会复用旧 policy.Set：
// 限流令牌桶与熔断计数因此得以跨配置发布延续，
// 避免每次发布都把在途流量当成新一轮冷启动。
func NewState(snap *config.Snapshot, prev *State) (*State, error) {
	if snap == nil || snap.Config() == nil {
		return nil, errors.New("server: snapshot 为 nil")
	}

	table, err := routing.NewTable(snap, nil)
	if err != nil {
		return nil, fmt.Errorf("构建路由表: %w", err)
	}

	cfg := snap.Config()
	st := &State{
		snapshot: snap,
		table:    table,
		policies: make(map[string]*policy.Set, table.RouteCount()),
	}

	for ti := range cfg.Tenants {
		tenant := &cfg.Tenants[ti]
		for si := range tenant.Services {
			svc := &tenant.Services[si]
			for ri := range svc.Routes {
				route := &svc.Routes[ri]
				set, err := policy.BuildSet(route.ID, svc, route)
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

	return st, nil
}

// Snapshot 返回当前生效的配置快照。
func (s *State) Snapshot() *config.Snapshot { return s.snapshot }

// Table 返回编译后的路由索引。
func (s *State) Table() *routing.Table { return s.table }

// Policy 返回路由对应的治理策略集合。
func (s *State) Policy(routeID string) *policy.Set { return s.policies[routeID] }

// RouteCount 返回已编译的路由条数。
func (s *State) RouteCount() int { return len(s.policies) }

// TenantCount 返回租户数量。
func (s *State) TenantCount() int { return len(s.snapshot.Config().Tenants) }

// ReusedPolicies 返回本次重建复用的策略数量。
func (s *State) ReusedPolicies() int { return s.reused }
