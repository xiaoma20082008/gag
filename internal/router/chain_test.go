package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"gag/internal/core"
	gerr "gag/pkg/errors"
)

func fakeHooks() Hooks {
	return Hooks{
		ResolveTenant: func(c *core.RequestContext) error {
			c.Tenant = "t1"
			return nil
		},
		ResolveRoute: func(c *core.RequestContext) error {
			c.RouteID = "r1"
			c.Instance = "127.0.0.1:1"
			return nil
		},
		Forward: func(c *core.RequestContext) error {
			c.SetResponse(http.StatusOK, http.Header{}, []byte("upstream"))
			return nil
		},
	}
}

func build(t *testing.T, hooks Hooks, register func(*core.Callbacks)) *Chain {
	t.Helper()
	cbs := core.NewCallbacks()
	if register != nil {
		register(cbs)
	}
	return NewChain(cbs, hooks)
}

func serve(t *testing.T, ch *Chain) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	ch.ServeHTTP(rec, req)
	return rec
}

// goOn 返回一个只计数的 handler。
func goOn(log *[]string, name string) func(*core.RequestContext) core.Action {
	return func(*core.RequestContext) core.Action {
		*log = append(*log, name)
		return core.ActionGoOn
	}
}

func TestChainOrderFollowsOrderNotRegistration(t *testing.T) {
	var calls []string
	ch := build(t, fakeHooks(), func(cbs *core.Callbacks) {
		// 故意逆序注册：执行顺序应由 order 决定。
		cbs.Add(core.HookBeforeRoute, 50, "mod_z", "z", goOn(&calls, "z"))
		cbs.Add(core.HookBeforeRoute, 10, "mod_a", "a", goOn(&calls, "a"))
		cbs.Add(core.HookBeforeRoute, 30, "mod_m", "m", goOn(&calls, "m"))
	})
	serve(t, ch)

	want := []string{"a", "m", "z"}
	if len(calls) != len(want) {
		t.Fatalf("执行序列 = %v, want %v", calls, want)
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Fatalf("执行序列 = %v, want %v", calls, want)
		}
	}
}

func TestChainFinishStopsRequestStages(t *testing.T) {
	var finished bool
	ch := build(t, fakeHooks(), func(cbs *core.Callbacks) {
		cbs.Add(core.HookBeforeRoute, 10, "mod_gate", "gate", func(c *core.RequestContext) core.Action {
			c.Fail(gerr.New(gerr.CodePermissionDenied, "denied"))
			return core.ActionFinish
		})
		cbs.Add(core.HookRequestFinish, 10, "mod_audit", "audit", func(*core.RequestContext) core.Action {
			finished = true
			return core.ActionGoOn
		})
	})
	rec := serve(t, ch)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if !finished {
		t.Fatal("core.HookRequestFinish 必须执行")
	}
}

func TestChainRespondBypassesRouting(t *testing.T) {
	tenantResolved := false
	hooks := fakeHooks()
	hooks.ResolveTenant = func(c *core.RequestContext) error {
		tenantResolved = true
		return nil
	}
	ch := build(t, hooks, func(cbs *core.Callbacks) {
		cbs.Add(core.HookBeforeRoute, 10, "mod_cors", "preflight", func(c *core.RequestContext) core.Action {
			c.SetResponse(http.StatusNoContent, http.Header{}, nil)
			return core.ActionRespond
		})
	})
	rec := serve(t, ch)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if tenantResolved {
		t.Fatal("core.ActionRespond 之后不应继续解析租户")
	}
}

func TestChainFinishAlwaysRunsOnPanic(t *testing.T) {
	var finished bool
	ch := build(t, fakeHooks(), func(cbs *core.Callbacks) {
		cbs.Add(core.HookBeforeRoute, 10, "mod_bad", "boom", func(*core.RequestContext) core.Action {
			panic("module exploded")
		})
		cbs.Add(core.HookRequestFinish, 10, "mod_audit", "audit", func(*core.RequestContext) core.Action {
			finished = true
			return core.ActionGoOn
		})
	})
	rec := serve(t, ch)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("panic 应转为 500，实际 %d", rec.Code)
	}
	if !finished {
		t.Fatal("panic 之后 core.HookRequestFinish 仍必须执行")
	}
}

func TestChainFinishAlwaysRunsWhenFinisherPanics(t *testing.T) {
	var secondRan bool
	ch := build(t, fakeHooks(), func(cbs *core.Callbacks) {
		cbs.Add(core.HookRequestFinish, 10, "mod_bad", "boom", func(*core.RequestContext) core.Action {
			panic("finisher exploded")
		})
		cbs.Add(core.HookRequestFinish, 20, "mod_log", "log", func(*core.RequestContext) core.Action {
			secondRan = true
			return core.ActionGoOn
		})
	})
	rec := serve(t, ch)

	if !secondRan {
		t.Fatal("单个收尾模块 panic 不应影响其余收尾模块")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestChainForwardRetriesAndFiresPerAttempt(t *testing.T) {
	hooks := fakeHooks()
	hooks.MaxAttempts = func(*core.RequestContext) int { return 3 }
	hooks.Retryable = func(*core.RequestContext, error, int) bool { return true }

	var attempts, observations int
	hooks.Forward = func(c *core.RequestContext) error {
		attempts++
		if attempts < 3 {
			return fmt.Errorf("transport blip")
		}
		c.SetResponse(http.StatusOK, http.Header{}, []byte("ok"))
		return nil
	}

	ch := build(t, hooks, func(cbs *core.Callbacks) {
		cbs.Add(core.HookForward, 10, "mod_obs", "observe", func(*core.RequestContext) core.Action {
			observations++
			return core.ActionGoOn
		})
	})
	rec := serve(t, ch)

	if attempts != 3 || observations != 3 {
		t.Fatalf("attempts=%d hookForward=%d, want 3/3", attempts, observations)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestChainForwardStopsWhenNotRetryable(t *testing.T) {
	hooks := fakeHooks()
	hooks.MaxAttempts = func(*core.RequestContext) int { return 3 }
	hooks.Retryable = func(*core.RequestContext, error, int) bool { return false }

	var attempts int
	hooks.Forward = func(*core.RequestContext) error {
		attempts++
		return gerr.New(gerr.CodeBadGateway, "downstream down")
	}

	ch := build(t, hooks, nil)
	rec := serve(t, ch)

	if attempts != 1 {
		t.Fatalf("不可重试时应只尝试 1 次，实际 %d", attempts)
	}
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", rec.Code)
	}
}

func TestChainForwardModuleCanRejectAttempt(t *testing.T) {
	hooks := fakeHooks()
	var forwarded int
	hooks.Forward = func(*core.RequestContext) error {
		forwarded++
		return nil
	}
	ch := build(t, hooks, func(cbs *core.Callbacks) {
		cbs.Add(core.HookForward, 10, "mod_breaker", "gate", func(c *core.RequestContext) core.Action {
			c.Fail(gerr.New(gerr.CodeCircuitOpen, "circuit open"))
			return core.ActionFinish
		})
	})
	rec := serve(t, ch)

	if forwarded != 0 {
		t.Fatal("熔断拒绝时不应真正调用下游")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestChainActionCloseWritesNothing(t *testing.T) {
	var finished bool
	ch := build(t, fakeHooks(), func(cbs *core.Callbacks) {
		cbs.Add(core.HookBeforeRoute, 10, "mod_guard", "drop", func(c *core.RequestContext) core.Action {
			c.CloseConn = true
			return core.ActionClose
		})
		cbs.Add(core.HookRequestFinish, 10, "mod_audit", "audit", func(*core.RequestContext) core.Action {
			finished = true
			return core.ActionGoOn
		})
	})
	rec := serve(t, ch)

	if rec.Body.Len() != 0 {
		t.Fatalf("core.ActionClose 不应写响应体: %q", rec.Body.String())
	}
	if !finished {
		t.Fatal("core.ActionClose 之后仍应记录收尾信息（客户端中断）")
	}
}

func TestCallbacksRejectDuplicatesAndNil(t *testing.T) {
	cbs := core.NewCallbacks()
	cbs.Add(core.HookBeforeRoute, 10, "mod_a", "a", func(*core.RequestContext) core.Action { return core.ActionGoOn })
	cbs.Add(core.HookBeforeRoute, 20, "mod_a", "b", func(*core.RequestContext) core.Action { return core.ActionGoOn })

	assertPanics(t, "重复注册", func() {
		cbs.Add(core.HookBeforeRoute, 30, "mod_a", "a", func(*core.RequestContext) core.Action { return core.ActionGoOn })
	})
	assertPanics(t, "nil handler", func() {
		cbs.Add(core.HookBeforeRoute, 40, "mod_c", "c", nil)
	})
	assertPanics(t, "非法回调点", func() {
		cbs.Add(core.HookPoint(99), 10, "mod_d", "d", func(*core.RequestContext) core.Action { return core.ActionGoOn })
	})
}

func TestChainRequiresCoreHooks(t *testing.T) {
	assertPanics(t, "缺少 ResolveRoute", func() {
		NewChain(core.NewCallbacks(), Hooks{
			ResolveTenant: func(*core.RequestContext) error { return nil },
			Forward:       func(*core.RequestContext) error { return nil },
		})
	})
	assertPanics(t, "缺少 Forward", func() {
		NewChain(core.NewCallbacks(), Hooks{
			ResolveTenant: func(*core.RequestContext) error { return nil },
			ResolveRoute:  func(*core.RequestContext) error { return nil },
		})
	})
}

func TestTopologyListsHandlersByHook(t *testing.T) {
	ch := build(t, fakeHooks(), func(cbs *core.Callbacks) {
		cbs.Add(core.HookBeforeRoute, 10, "mod_a", "a", func(*core.RequestContext) core.Action { return core.ActionGoOn })
		cbs.Add(core.HookRequestFinish, 10, "mod_log", "log", func(*core.RequestContext) core.Action { return core.ActionGoOn })
	})
	topo := ch.Topology()
	if got := topo["before_route"]; len(got) != 1 || got[0] != "mod_a.a" {
		t.Fatalf("before_route = %v", got)
	}
	if got := topo["request_finish"]; len(got) != 1 || got[0] != "mod_log.log" {
		t.Fatalf("request_finish = %v", got)
	}
	if got := topo["forward"]; len(got) != 0 {
		t.Fatalf("forward 应为空: %v", got)
	}
}

// TestChainReadResponseRunsAfterShortCircuit 锁住收尾顺序：
// 无论请求是被模块短路（Respond）、治理拒绝（Finish）还是 panic，
// read_response 都必须执行——否则响应改写模块只覆盖成功路径，
// 被拒绝的请求会漏掉 CORS 头与 hop-by-hop 清理。
func TestChainReadResponseRunsAfterShortCircuit(t *testing.T) {
	cases := []struct {
		name string
		hook func(*core.Callbacks)
		want int
	}{
		{
			name: "respond",
			hook: func(cbs *core.Callbacks) {
				cbs.Add(core.HookBeforeRoute, 10, "mod_short", "short", func(c *core.RequestContext) core.Action {
					c.SetResponse(http.StatusNoContent, http.Header{}, nil)
					return core.ActionRespond
				})
			},
			want: http.StatusNoContent,
		},
		{
			name: "finish",
			hook: func(cbs *core.Callbacks) {
				cbs.Add(core.HookAfterRoute, 10, "mod_reject", "reject", func(c *core.RequestContext) core.Action {
					c.Fail(gerr.Errorf(gerr.CodeUnauthenticated, "nope"))
					return core.ActionFinish
				})
			},
			want: http.StatusUnauthorized,
		},
		{
			name: "panic",
			hook: func(cbs *core.Callbacks) {
				cbs.Add(core.HookBeforeRoute, 10, "mod_bad", "boom", func(*core.RequestContext) core.Action {
					panic("exploded")
				})
			},
			want: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var readResponse bool
			ch := build(t, fakeHooks(), func(cbs *core.Callbacks) {
				tc.hook(cbs)
				cbs.Add(core.HookReadResponse, 10, "mod_resp", "resp", func(c *core.RequestContext) core.Action {
					readResponse = true
					// 走到这里时响应必须已经就绪，改写才有意义。
					if c.Response == nil {
						t.Error("read_response 执行时 c.Response 不应为 nil")
					}
					if c.Response.Status != tc.want {
						t.Errorf("read_response 看到的 status = %d, want %d", c.Response.Status, tc.want)
					}
					return core.ActionGoOn
				})
			})
			rec := serve(t, ch)

			if !readResponse {
				t.Fatalf("%s 短路后 read_response 未执行", tc.name)
			}
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

// TestChainBackoffWaitsBetweenAttempts 验证退避由框架驱动：
// 模块只回答"要不要重试/等多久"，循环本身在链里。
func TestChainBackoffWaitsBetweenAttempts(t *testing.T) {
	hooks := fakeHooks()
	hooks.MaxAttempts = func(*core.RequestContext) int { return 2 }
	hooks.Retryable = func(*core.RequestContext, error, int) bool { return true }
	hooks.Backoff = func(*core.RequestContext, int) time.Duration { return 20 * time.Millisecond }
	hooks.Forward = func(*core.RequestContext) error {
		return fmt.Errorf("transport blip")
	}

	start := time.Now()
	ch := build(t, hooks, nil)
	serve(t, ch)
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("未按 Backoff 等待，elapsed = %v", elapsed)
	}
}

// TestChainCloseSkipsReadResponse 对应设计文档的动作矩阵：
// 连接已经不在，响应阶段没有意义，但收尾仍要执行。
func TestChainCloseSkipsReadResponse(t *testing.T) {
	var readResponse, finished bool
	ch := build(t, fakeHooks(), func(cbs *core.Callbacks) {
		cbs.Add(core.HookBeforeRoute, 10, "mod_close", "close", func(*core.RequestContext) core.Action {
			return core.ActionClose
		})
		cbs.Add(core.HookReadResponse, 10, "mod_resp", "resp", func(*core.RequestContext) core.Action {
			readResponse = true
			return core.ActionGoOn
		})
		cbs.Add(core.HookRequestFinish, 10, "mod_log", "log", func(*core.RequestContext) core.Action {
			finished = true
			return core.ActionGoOn
		})
	})
	rec := serve(t, ch)

	if readResponse {
		t.Fatal("core.ActionClose 后不应执行 read_response")
	}
	if !finished {
		t.Fatal("core.ActionClose 后 request_finish 仍必须执行")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200（Close 下不写响应）", rec.Code)
	}
}

func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s：期望 panic", name)
		}
	}()
	fn()
}
