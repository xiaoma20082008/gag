package proxy

import "context"

type HttpInvoker struct{}

func (inv *HttpInvoker) InvokeWithCtx(ctx context.Context, req *InvocationRequest) (*InvocationResponse, error) {
	return nil, nil
}
