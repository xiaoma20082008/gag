package proxy

import "context"

type InvocationRequest struct{}

type InvocationResponse struct{}

type Invoker interface {
	InvokeWithCtx(ctx context.Context, req *InvocationRequest) (*InvocationResponse, error)
}
