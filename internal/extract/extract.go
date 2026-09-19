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
	var llmErr string
	useLLM := strings.TrimSpace(cfg.APIKey) != "" && s.allowLLM(project)
	if useLLM {
		client := s.client()
		prompt := BuildPrompt(project, existing, turns)
		props, err := client.Complete(ctx, prompt)
		if err == nil {
			s.markLLM(project)
			if len(props) == 0 {
				// Free models sometimes return empty even for clear decisions;
				// keep durable heuristic hits so we do not drop real facts.
				if h := Heuristic(project, turns, existing); len(h) > 0 {
					return Result{Proposals: h, Provider: ProviderHeuristic}, nil
				}
			}
			return Result{Proposals: props, Provider: ProviderOpenRouter}, nil
		}
		llmErr = err.Error()
		// Network/parse failure: do not burn the throttle; heuristic fallback.
	}

	props := Heuristic(project, turns, existing)
	return Result{Proposals: props, Provider: ProviderHeuristic, LLMError: llmErr}, nil
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
