package extract

import (
	"context"
	"strings"
)

// Extract runs OpenRouter when configured and allowed; otherwise heuristic.
// Prefer ExtractLLMOnly when OPENROUTER_API_KEY is set and heuristic scrap
// must not pollute the portal.
func (s *Service) Extract(ctx context.Context, project string, turns []Turn, existing []Existing) (Result, error) {
	turns = CapTurns(turns)
	if len(turns) == 0 {
		return Result{Provider: ProviderHeuristic}, nil
	}
	project = strings.TrimSpace(project)
	if project == "" {
		project = "unknown"
	}

	cfg := Config{}
	if s != nil {
		cfg = s.Cfg
	}
	var llmErr string
	useLLM := strings.TrimSpace(cfg.APIKey) != "" && s.allowLLM(project)
	if useLLM {
		client := s.client()
		prompt := BuildPrompt(project, existing, turns)
		props, err := client.Complete(ctx, prompt)
		if err == nil {
			s.markLLM(project)
			return Result{Proposals: props, Provider: ProviderOpenRouter}, nil
		}
		llmErr = err.Error()
	}

	if strings.TrimSpace(cfg.APIKey) != "" {
		// Key configured: never emit heuristic scrap (empty > junk).
		provider := ProviderOpenRouter
		if llmErr == "" {
			provider = "throttled"
			llmErr = "openrouter throttle"
		}
		return Result{Provider: provider, LLMError: llmErr}, nil
	}

	props := Heuristic(project, turns, existing)
	return Result{Proposals: props, Provider: ProviderHeuristic, LLMError: llmErr}, nil
}

// ExtractLLMOnly always uses OpenRouter when a key is set; never heuristic.
func (s *Service) ExtractLLMOnly(ctx context.Context, project string, turns []Turn, existing []Existing) (Result, error) {
	turns = CapTurns(turns)
	if len(turns) == 0 {
		return Result{Provider: ProviderOpenRouter}, nil
	}
	if s == nil || strings.TrimSpace(s.Cfg.APIKey) == "" {
		return Result{Provider: ProviderOpenRouter, LLMError: "OPENROUTER_API_KEY unset"}, nil
	}
	project = strings.TrimSpace(project)
	if project == "" {
		project = "unknown"
	}
	client := s.client()
	prompt := BuildPrompt(project, existing, turns)
	props, err := client.Complete(ctx, prompt)
	if err != nil {
		return Result{Provider: ProviderOpenRouter, LLMError: err.Error()}, nil
	}
	s.markLLM(project)
	return Result{Proposals: props, Provider: ProviderOpenRouter}, nil
}

func (s *Service) client() *Client {
	if s != nil && s.Client != nil {
		return s.Client
	}
	cfg := Config{}
	if s != nil {
		cfg = s.Cfg
	}
	return &Client{Cfg: cfg}
}

// HasAPIKey reports whether OpenRouter LLM path can run.
func (s *Service) HasAPIKey() bool {
	return s != nil && strings.TrimSpace(s.Cfg.APIKey) != ""
}
