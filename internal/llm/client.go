package llm

import "context"

type ChatRequest struct{}

type ChatResponse struct{}

type LLMClient interface {
	ChatWithCtx(ctx context.Context, req *ChatRequest) (*ChatResponse, error)
}

type ClientConfig struct {
	BaseUrl string
	ApiKey  string
	Token   string
}

func NewClientConfig() *ClientConfig {
	return &ClientConfig{}
}

func NewLLMClient(cfg *ClientConfig) LLMClient {
	return nil
}
