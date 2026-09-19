package extract

import (
	"context"
	"strings"
)

// Extract runs OpenRouter when configured and allowed; otherwise heuristic.
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
	useLLM := strings.TrimSpace(cfg.APIKey) != "" && s.allowLLM(project)
	if useLLM {
		client := s.client()
		prompt := BuildPrompt(project, existing, turns)
		props, err := client.Complete(ctx, prompt)
		if err == nil {
			// Successful LLM call wins even when empty — prefer "nothing
			// durable" over heuristic re-scraping the same turns as junk.
			return Result{Proposals: props, Provider: ProviderOpenRouter}, nil
		}
		// Network/parse failure: heuristic fallback keeps extraction alive.
		_ = err
	}

	props := Heuristic(project, turns, existing)
	return Result{Proposals: props, Provider: ProviderHeuristic}, nil
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
