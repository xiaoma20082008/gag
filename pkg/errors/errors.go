package errors

import (
	"fmt"
	"net/http"
	"time"
)

// Code 是网关对外暴露的稳定错误标识。
type Code string

const (
	CodeInvalidArgument     Code = "invalid_argument"
	CodeUnauthenticated     Code = "unauthenticated"
	CodePermissionDenied    Code = "permission_denied"
	CodeNotFound            Code = "not_found"
	CodePayloadTooLarge     Code = "payload_too_large"
	CodeRateLimited         Code = "rate_limited"
	CodeCircuitOpen         Code = "circuit_open"
	CodeUpstreamTimeout     Code = "upstream_timeout"
	CodeUpstreamUnavailable Code = "upstream_unavailable"
	CodeBadGateway          Code = "bad_gateway"
	CodeNotImplemented      Code = "not_implemented"
	CodeInternal            Code = "internal_error"
)

var codeStatus = map[Code]int{
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

// Status 返回错误码对应的 HTTP 状态码。
func (c Code) Status() int {
	if status, ok := codeStatus[c]; ok {
		return status
	}
	return http.StatusInternalServerError
}

// Error 是请求链路中传递的结构化错误。
type Error struct {
	Code       Code
	Status     int
	Message    string
	RetryAfter time.Duration
	Cause      error
}

// New 构造一个错误，HTTP 状态码由 Code 推导。
func New(code Code, message string) *Error {
	return &Error{Code: code, Status: code.Status(), Message: message}
}

func Errorf(code Code, format string, args ...any) *Error {
	return New(code, fmt.Sprintf(format, args...))
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
	if e := findError(err, 0); e != nil {
		return e
	}
	return New(CodeInternal, "internal error").WithCause(err)
}

// findError 沿 Unwrap 链找第一个 *Error。
func findError(err error, depth int) *Error {
	if err == nil || depth > 32 {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range multi.Unwrap() {
			if e := findError(child, depth+1); e != nil {
				return e
			}
		}
		return nil
	}
	if single, ok := err.(interface{ Unwrap() error }); ok {
		return findError(single.Unwrap(), depth+1)
	}
	return nil
}
