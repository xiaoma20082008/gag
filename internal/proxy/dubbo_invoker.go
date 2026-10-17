package proxy

import "context"

type DubboInvoker struct{}

func (inv *DubboInvoker) InvokeWithCtx(ctx context.Context, req *InvocationRequest) (*InvocationResponse, error) {
	return nil, nil
}
