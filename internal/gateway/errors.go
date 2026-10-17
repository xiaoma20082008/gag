package gateway

import (
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ErrorCode 是网关对外暴露的稳定错误标识。
//
// 客户端与监控都应该基于 Code 而不是 HTTP Status 做判断：
// 同一个 Status 可能对应多种语义，而 Code 是契约的一部分。
type ErrorCode string

const (
	CodeInvalidArgument     ErrorCode = "invalid_argument"
	CodeUnauthenticated     ErrorCode = "unauthenticated"
	CodePermissionDenied    ErrorCode = "permission_denied"
	CodeNotFound            ErrorCode = "not_found"
	CodePayloadTooLarge     ErrorCode = "payload_too_large"
	CodeRateLimited         ErrorCode = "rate_limited"
	CodeCircuitOpen         ErrorCode = "circuit_open"
	CodeUpstreamTimeout     ErrorCode = "upstream_timeout"
	CodeUpstreamUnavailable ErrorCode = "upstream_unavailable"
	CodeBadGateway          ErrorCode = "bad_gateway"
	CodeNotImplemented      ErrorCode = "not_implemented"
	CodeInternal            ErrorCode = "internal_error"
)

var codeStatus = map[ErrorCode]int{
	CodeInvalidArgument:     http.StatusBadRequest,
	CodeUnauthenticated:     http.StatusUnauthorized,
	CodePermissionDenied:    http.StatusForbidden,
	CodeNotFound:            http.StatusNotFound,
	CodePayloadTooLarge:     http.StatusRequestEntityTooLarge,
	CodeRateLimited:         http.StatusTooManyRequests,
	CodeCircuitOpen:         http.StatusServiceUnavailable,
	CodeUpstreamTimeout:     http.StatusGatewayTimeout,
	CodeUpstreamUnavailable: http.StatusServiceUnavailable,
	CodeBadGateway:          http.StatusBadGateway,
	CodeNotImplemented:      http.StatusNotImplemented,
	CodeInternal:            http.StatusInternalServerError,
}

// Error 是请求链路中传递的结构化错误。
type Error struct {
	Code       ErrorCode
	Status     int
	Message    string
	RetryAfter time.Duration
	Cause      error
}

// NewError 构造一个错误，HTTP 状态码由 Code 推导。
func NewError(code ErrorCode, message string) *Error {
	status, ok := codeStatus[code]
	if !ok {
		status = http.StatusInternalServerError
	}
	return &Error{Code: code, Status: status, Message: message}
}

// Errorf 是 NewError 的格式化版本。
func Errorf(code ErrorCode, format string, args ...any) *Error {
	return NewError(code, fmt.Sprintf(format, args...))
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Cause != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Cause)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Cause }

// WithCause 附加底层错误，便于日志定位。
func (e *Error) WithCause(err error) *Error {
	if e == nil {
		return nil
	}
	clone := *e
	clone.Cause = err
	return &clone
}

// WithStatus 覆盖默认状态码。
func (e *Error) WithStatus(status int) *Error {
	if e == nil {
		return nil
	}
	clone := *e
	clone.Status = status
	return &clone
}

// WithRetryAfter 标记建议的重试间隔，会写入 Retry-After 响应头。
func (e *Error) WithRetryAfter(d time.Duration) *Error {
	if e == nil {
		return nil
	}
	clone := *e
	clone.RetryAfter = d
	return &clone
}

// AsError 把任意 error 归一化为 *Error，未知错误按内部错误处理。
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var ge *Error
	if errors.As(err, &ge) {
		return ge
	}
	return NewError(CodeInternal, "internal error").WithCause(err)
}
