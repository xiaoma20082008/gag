package gateway

import (
	"context"

	"gag/internal/config"
)

func (g *Gateway) startWatchers() {
	ctx, cancel := context.WithCancel(context.Background())
	g.watchCancel = cancel

	g.watchStore(ctx)
	g.watchSource(ctx)
}

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

func (g *Gateway) watchSource(ctx context.Context) {
	if g.cc == nil {
		return
	}
	g.cc.OnChange(ctx, g.ccc.Path, func(raw string) {
		next, err := config.SnapshotFrom([]byte(raw), g.ccc.Path, "")
		if err != nil {
			g.logger.Error("配置来源变更但解析失败，保留当前配置", map[string]any{
				"source": g.cc.Type(),
				"key":    g.ccc.Path,
				"error":  err.Error(),
			})
			g.metrics.ConfigApply.WithLabelValues("error").Inc()
			return
		}
		g.apply(next)
	})
}

func (g *Gateway) apply(next *config.Snapshot) {
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

func (g *Gateway) stopWatcher() {
	if g.watchCancel == nil {
		return
	}
	g.watchCancel()
	g.watchWG.Wait()
	g.watchCancel = nil
}
