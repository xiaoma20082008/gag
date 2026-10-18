package proxy

import (
	"context"
	"fmt"
)

// DubboInvoker 是 Dubbo 协议的占位实现。
type DubboInvoker struct{}

func (*DubboInvoker) Protocol() string { return "dubbo" }

func (*DubboInvoker) InvokeWithCtx(_ context.Context, _ *InvocationRequest) (*InvocationResponse, error) {
	return nil, fmt.Errorf("%w: dubbo", ErrUnsupported)
}

var _ Invoker = (*DubboInvoker)(nil)

func NewDubboInvoker() *DubboInvoker {
	return &DubboInvoker{}
}
