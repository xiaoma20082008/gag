package errors

import (
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestCodeStatusMapping(t *testing.T) {
	cases := []struct {
		code   Code
		status int
	}{
		{CodeInvalidArgument, http.StatusBadRequest},
		{CodeUnauthenticated, http.StatusUnauthorized},
		{CodePermissionDenied, http.StatusForbidden},
		{CodeNotFound, http.StatusNotFound},
		{CodePayloadTooLarge, http.StatusRequestEntityTooLarge},
		{CodeRateLimited, http.StatusTooManyRequests},
		{CodeCircuitOpen, http.StatusServiceUnavailable},
		{CodeUpstreamTimeout, http.StatusGatewayTimeout},
		// 与 circuit_open 同为 503，但语义不同：容量问题 vs 熔断保护。
		{CodeUpstreamUnavailable, http.StatusServiceUnavailable},
		{CodeBadGateway, http.StatusBadGateway},
		{CodeNotImplemented, http.StatusNotImplemented},
		{CodeInternal, http.StatusInternalServerError},
	}
	for _, c := range cases {
		if got := c.code.Status(); got != c.status {
			t.Errorf("%s.Status() = %d, want %d", c.code, got, c.status)
		}
	}
}

// 未知错误码不能返回 0：0 不是合法状态码，会让响应写出失败。
func TestUnknownCodeFallsBackTo500(t *testing.T) {
	if got := Code("no_such_code").Status(); got != http.StatusInternalServerError {
		t.Fatalf("未知 Code 的状态码 = %d, want 500", got)
	}
}

func TestNewDerivesStatusFromCode(t *testing.T) {
	e := New(CodeRateLimited, "慢点")
	if e.Status != http.StatusTooManyRequests {
		t.Fatalf("Status = %d, want 429", e.Status)
	}
	if e.Message != "慢点" {
		t.Fatalf("Message = %q", e.Message)
	}
}

// With* 必须返回新值：同一个错误会被链路上多个模块读到，
// 原地修改会让"谁改的"无法追溯，也会让并发读出现撕裂。
func TestWithMethodsAreImmutable(t *testing.T) {
	base := New(CodeBadGateway, "下游失败")

	caused := base.WithCause(io.EOF)
	if base.Cause != nil {
		t.Fatal("WithCause 修改了原错误")
	}
	if caused.Cause == nil {
		t.Fatal("WithCause 没有把 cause 写进副本")
	}

	status := base.WithStatus(http.StatusTeapot)
	if base.Status != http.StatusBadGateway {
		t.Fatal("WithStatus 修改了原错误")
	}
	if status.Status != http.StatusTeapot {
		t.Fatalf("副本 Status = %d", status.Status)
	}

	limited := base.WithRetryAfter(2 * time.Second)
	if base.RetryAfter != 0 {
		t.Fatal("WithRetryAfter 修改了原错误")
	}
	if limited.RetryAfter != 2*time.Second {
		t.Fatalf("副本 RetryAfter = %v", limited.RetryAfter)
	}
}

func TestErrorString(t *testing.T) {
	if got, want := New(CodeNotFound, "没找到").Error(), "not_found: 没找到"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
	e := New(CodeBadGateway, "下游失败").WithCause(io.EOF)
	if got, want := e.Error(), "bad_gateway: 下游失败: EOF"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

func TestAsErrorNil(t *testing.T) {
	if got := AsError(nil); got != nil {
		t.Fatalf("AsError(nil) = %v, want nil", got)
	}
}

func TestAsErrorReturnsOriginal(t *testing.T) {
	origin := New(CodeRateLimited, "限流")
	if got := AsError(origin); got != origin {
		t.Fatal("AsError 应该原样返回 *Error，而不是包一层")
	}
}

// fmt.Errorf("%w") 包装后仍要能取到原始 Code——
// 否则 router 里 mapLookupError 的 401/404/502 区分会被中间层包装吃掉。
func TestAsErrorUnwrapsSingleChain(t *testing.T) {
	origin := New(CodeNotFound, "路由未匹配")
	wrapped := fmt.Errorf("router: %w", fmt.Errorf("resolve: %w", origin))

	got := AsError(wrapped)
	if got.Code != CodeNotFound {
		t.Fatalf("Code = %s, want not_found", got.Code)
	}
	if got.Status != http.StatusNotFound {
		t.Fatalf("Status = %d, want 404", got.Status)
	}
}

// errors.Join 产生的是多值 Unwrap，单值遍历会漏掉它；
// cmd/main 停机时正是用 Join 聚合两个端口的错误。
func TestAsErrorUnwrapsJoinedErrors(t *testing.T) {
	origin := New(CodeUpstreamUnavailable, "没有实例")
	joined := fmt.Errorf("shutdown: %w", joinErrs(io.EOF, origin))

	got := AsError(joined)
	if got.Code != CodeUpstreamUnavailable {
		t.Fatalf("Code = %s, want upstream_unavailable", got.Code)
	}
}

// 非 *Error 的错误按内部错误处理，但原始错误要留在 Cause 里供日志定位。
func TestAsErrorUnknownBecomesInternal(t *testing.T) {
	origin := io.ErrUnexpectedEOF
	got := AsError(origin)

	if got.Code != CodeInternal {
		t.Fatalf("Code = %s, want internal_error", got.Code)
	}
	if got.Status != http.StatusInternalServerError {
		t.Fatalf("Status = %d, want 500", got.Status)
	}
	if got.Cause != origin {
		t.Fatalf("Cause = %v, want 原始错误", got.Cause)
	}
}

// 自引用的 Unwrap 不能把 AsError 挂死。
func TestAsErrorSurvivesCyclicUnwrap(t *testing.T) {
	loop := &cyclicError{}
	loop.self = loop

	got := AsError(loop)
	if got == nil || got.Code != CodeInternal {
		t.Fatalf("AsError(cyclic) = %v", got)
	}
}

type cyclicError struct{ self error }

func (e *cyclicError) Error() string { return "cyclic" }
func (e *cyclicError) Unwrap() error { return e.self }

// joinErrs 用标准库 errors.Join 的等价实现，避免本包导入同名包。
func joinErrs(errs ...error) error { return joinedErrors(errs) }

type joinedErrors []error

func (j joinedErrors) Error() string { return fmt.Sprint([]error(j)) }
func (j joinedErrors) Unwrap() []error {
	return []error(j)
}
