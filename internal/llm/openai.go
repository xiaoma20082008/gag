package llm

type OpenaiLLMClient struct {
	cfg *ClientConfig
}

func NewOpenaiLLMClient(cfg *ClientConfig) *OpenaiLLMClient {
	return &OpenaiLLMClient{
		cfg: cfg,
	}
}
