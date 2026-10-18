package gateway

import (
	"context"

	"gag/internal/config"
)

// startWatchers 订阅两路配置变更：控制面发布 与 配置来源自身的变化。
func (g *Gateway) startWatchers() {
	ctx, cancel := context.WithCancel(context.Background())
	g.watchCancel = cancel

	g.watchStore(ctx)
	g.watchSource(ctx)
}

// watchStore 订阅配置仓库的变更，把新快照原子替换进数据面。
//
// Store 刻意不关闭 channel（避免与发布方竞争），因此退出条件只能由
// 消费方从 ctx 感知——这也是这里不用 for range 的原因。
func (g *Gateway) watchStore(ctx context.Context) {
	updates := g.store.Watch(ctx)
	g.watchWG.Add(1)
	go func() {
		defer g.watchWG.Done()
		for {
			var next *config.Snapshot
			select {
			case <-ctx.Done():
				return
			case next = <-updates:
			}
			if next == nil {
				continue
			}
			g.apply(next)
		}
	}()
}

// watchSource 订阅配置来源（文件改动 / nacos 推送）。
//
// 拿到的是文档原文，因此要重新解析一遍；解析失败只记日志、保留旧配置，
// 绝不能让一份写坏的配置把正在服务的版本顶掉。
func (g *Gateway) watchSource(ctx context.Context) {
	if g.cc == nil {
		return
	}
	g.cc.OnChange(ctx, g.key, func(raw string) {
		next, err := config.SnapshotFrom([]byte(raw), g.key, g.format)
		if err != nil {
			g.logger.Error("配置来源变更但解析失败，保留当前配置", map[string]any{
				"source": g.cc.Type(),
				"key":    g.key,
				"error":  err.Error(),
			})
			g.metrics.ConfigApply.WithLabelValues("error").Inc()
			return
		}
		g.apply(next)
	})
}

// apply 落地一份新配置。
//
// 失败时保留旧配置：宁可跑旧版本，也不要半可用。发布方会从
// /admin/config 的响应里看到错误，但数据面自己不做回滚——
// 回滚由控制面的 Store 负责，两处都回滚会打架。
func (g *Gateway) apply(next *config.Snapshot) {
	// 内容没变就跳过：改个注释、调整缩进都会让文件变化，但重建路由表、
	// 重新预热实例来源是实打实的开销（每个集群都要查一次注册中心）。
	// Hash 算的是解析后的结构，不是原文，所以这类改动会被识别成"没变"。
	if cur := g.Data.State(); cur != nil && cur.Snapshot().Hash() == next.Hash() {
		g.logger.Info("配置内容未变化，跳过热加载", map[string]any{
			"version": next.Version(),
			"hash":    next.Hash()[:12],
		})
		return
	}

	if err := g.Data.Apply(next); err != nil {
		g.logger.Error("热加载失败，保留当前配置", map[string]any{
			"version": next.Version(),
			"error":   err.Error(),
		})
		g.metrics.ConfigApply.WithLabelValues("error").Inc()
		return
	}

	st := g.Data.State()
	g.logger.Info("配置已热加载", map[string]any{
		"version": next.Version(),
		"hash":    next.Hash()[:12],
		"routes":  st.RouteCount(),
		"reused":  st.ReusedPolicies(),
	})
	g.metrics.ConfigApply.WithLabelValues("ok").Inc()
}

// stopWatcher 停止热加载并等待消费 goroutine 退出。
//
// 必须等 goroutine 真正结束：否则停机后它可能还在往正在关闭的
// 数据面里写配置。来源侧的轮询由 client.Close 负责收尾。
func (g *Gateway) stopWatcher() {
	if g.watchCancel == nil {
		return
	}
	g.watchCancel()
	g.watchWG.Wait()
	g.watchCancel = nil
}
