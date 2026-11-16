package config

import (
	"log/slog"

	"github.com/resumeee-app/core/llm"
	"github.com/resumeee-app/core/llm/anthropic"
	"github.com/resumeee-app/core/llm/gemini"
	"github.com/resumeee-app/core/llm/openai"
	"github.com/resumeee-app/core/llm/openaicompat"
)

// RegisterLLMClients populates registry with every LLM provider client configured on c.
func (c *Config) RegisterLLMClients(registry *llm.Registry) {
	if c == nil || registry == nil {
		return
	}
	if c.GeminiAPIKey != "" {
		registry.Register(gemini.New(c.GeminiAPIKey, gemini.WithTimeout(c.LLMTimeout)))
	}
	if c.OpenAIAPIKey != "" {
		registry.Register(openai.New(c.OpenAIAPIKey, openai.WithTimeout(c.LLMTimeout)))
	}
	if c.AnthropicAPIKey != "" {
		registry.Register(anthropic.New(c.AnthropicAPIKey, anthropic.WithTimeout(c.LLMTimeout)))
	}
	if c.OpenAICompatBaseURL != "" {
		registry.Register(openaicompat.New(c.OpenAICompatBaseURL, c.OpenAICompatAPIKey, openaicompat.WithTimeout(c.LLMTimeout)))
	}
}

// NewLLMRegistry constructs an llm.Registry configured with logger and all non-empty providers on c.
func (c *Config) NewLLMRegistry(logger *slog.Logger) *llm.Registry {
	reg := llm.NewRegistry(llm.WithLogger(logger))
	c.RegisterLLMClients(reg)
	return reg
}
