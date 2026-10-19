package proxy

import (
	"context"
	"gag/internal/llm"
)

type LLMInvoker struct {
	client llm.LLMClient
}

func (invoker *LLMInvoker) Protocol() string { return "llm" }

func (invoker *LLMInvoker) InvokeWithCtx(ctx context.Context, req *InvocationRequest) (*InvocationResponse, error) {
	resp, err := invoker.client.ChatWithCtx(ctx, invoker.mapRequestToChatRequest(req))
	if err != nil {
		return nil, err
	}
	return invoker.mapChatResponseToResponse(resp), nil
}

func (invoker *LLMInvoker) mapRequestToChatRequest(req *InvocationRequest) *llm.ChatRequest {
	return nil
}

func (invoker *LLMInvoker) mapChatResponseToResponse(resp *llm.ChatResponse) *InvocationResponse {
	return nil
}

func NewLLMInvoker() *LLMInvoker {
	return &LLMInvoker{}
}

var _ Invoker = (*LLMInvoker)(nil)
