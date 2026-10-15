package llm

import "context"

type ChatRequest struct{}

type ChatResponse struct{}

type ClientConfig struct{}

type LLMClient interface {
	ChatWithCtx(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
}

func NewClientConfig() *ClientConfig {
	return &ClientConfig{}
}

func NewLLMClient(cfg *ClientConfig) LLMClient {
	return nil
}
