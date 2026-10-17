package llm

import (
	"context"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/openai/openai-go/v3/responses"
)

type OpenaiLLMClient struct {
	sdk openai.Client
	cfg *ClientConfig
}

func (client *OpenaiLLMClient) ChatWithCtx(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	res, err := client.sdk.Responses.New(ctx, client.mapToRequest(req))
	if err != nil {
		return nil, err
	}
	return client.mapToResponse(res)
}

func (client *OpenaiLLMClient) mapToRequest(req *ChatRequest) responses.ResponseNewParams {
	return responses.ResponseNewParams{}
}

func (client *OpenaiLLMClient) mapToResponse(res *responses.Response) (resp *ChatResponse, err error) {
	return
}

func NewOpenaiLLMClient(cfg *ClientConfig) *OpenaiLLMClient {
	return &OpenaiLLMClient{
		cfg: cfg,
		sdk: openai.NewClient(option.WithAPIKey(cfg.ApiKey)),
	}
}
