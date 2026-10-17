package proxy

import (
	"context"
	"fmt"
)

// DubboInvoker 是 Dubbo 协议的占位实现。
//
// 网关的协议适配层已经把差异收敛到 Invoker 接口，
// 接入 dubbo-go 时只需在这里实现序列化与调用，不影响治理链路。
type DubboInvoker struct{}

func (*DubboInvoker) Protocol() string { return "dubbo" }

func (*DubboInvoker) InvokeWithCtx(_ context.Context, _ *InvocationRequest) (*InvocationResponse, error) {
	return nil, fmt.Errorf("%w: dubbo", ErrUnsupported)
}

var _ Invoker = (*DubboInvoker)(nil)
