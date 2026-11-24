package continuex

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

// Provider messages can contain credentials or request bodies. Classify them
// into actionable UI errors without rendering their raw contents in chat.
func openCodeProblem(raw json.RawMessage) error {
	if problem := providerDiagnostic("opencode", string(raw)); problem != nil {
		return problem
	}
	return errors.New("OpenCode could not generate a reply. Check the provider connection and selected model, then retry explicitly.")
}

type openCodeSessionStatus struct {
	Type    string `json:"type"`
	Attempt int    `json:"attempt"`
	Message string `json:"message"`
	Next    int64  `json:"next"`
}

func openCodeStatusHealth(status openCodeSessionStatus, now time.Time) (string, error) {
	if status.Type != "retry" {
		return "", nil
	}
	wait := time.Duration(0)
	if status.Next > 0 {
		wait = time.UnixMilli(status.Next).Sub(now)
		if wait < 0 {
			wait = 0
		}
	}
	return retryDiagnostic("opencode", status.Message, status.Attempt, wait)
}

func openCodeEventProblem(event openCodeEvent, session string, ignored ...map[string]bool) error {
	var props struct {
		SessionID string                `json:"sessionID"`
		Error     json.RawMessage       `json:"error"`
		Status    openCodeSessionStatus `json:"status"`
		Part      openCodePart          `json:"part"`
		Info      struct {
			ID        string          `json:"id"`
			SessionID string          `json:"sessionID"`
			Error     json.RawMessage `json:"error"`
		} `json:"info"`
	}
	if json.Unmarshal(event.Properties, &props) != nil {
		return nil
	}
	sid := props.SessionID
	if sid == "" {
		sid = props.Info.SessionID
	}
	if sid == "" {
		sid = props.Part.SessionID
	}
	if sid != session {
		return nil
	}
	if len(ignored) > 0 && (ignored[0][props.Info.ID] || ignored[0][props.Part.MessageID]) {
		return nil
	}
	switch event.Type {
	case "message.part.updated":
		if props.Part.Type == "retry" {
			_, err := retryDiagnostic("opencode", string(props.Part.Error), props.Part.Attempt, 0)
			return err
		}
	case "session.status":
		_, err := openCodeStatusHealth(props.Status, time.Now())
		return err
	case "permission.asked", "permission.updated":
		return errors.New("OpenCode needs permission to continue. Review the request in OpenCode; Nexus has not approved it automatically.")
	case "question.asked":
		return errors.New("OpenCode is waiting for your answer to a question. Open the session in OpenCode to answer it.")
	case "session.error", "session.execution.failed":
		return openCodeProblem(props.Error)
	case "message.updated":
		if len(props.Info.Error) > 0 && string(props.Info.Error) != "null" {
			return openCodeProblem(props.Info.Error)
		}
	}
	return nil
}

func validateOpenCodeModel(ctx context.Context, client *http.Client, base, password string, input *openCodeInput, history []openCodeMessage) error {
	model := input.model
	if model == "" && len(history) == 0 {
		var configured struct {
			Model string `json:"model"`
		}
		cfgCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := openCodeJSON(cfgCtx, client, base, password, http.MethodGet, "/config", nil, &configured)
		cancel()
		if err == nil {
			model = configured.Model
		}
	}
	if model == "" {
		for i := len(history) - 1; i >= 0; i-- {
			info := history[i].Info
			if info.Model.ProviderID != "" && info.Model.ModelID != "" {
				model = info.Model.ProviderID + "/" + info.Model.ModelID
				break
			}
			if info.ProviderID != "" && info.ModelID != "" {
				model = info.ProviderID + "/" + info.ModelID
				break
			}
		}
	}
	if model == "" {
		return nil
	}
	var catalog struct {
		All []struct {
			ID     string                     `json:"id"`
			Models map[string]json.RawMessage `json:"models"`
		} `json:"all"`
		Connected []string `json:"connected"`
	}
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := openCodeJSON(checkCtx, client, base, password, http.MethodGet, "/provider", nil, &catalog); err != nil {
		var api *openCodeAPIError
		if errors.As(err, &api) && api.status == http.StatusNotFound {
			return nil
		} // Older API; runtime errors still surface.
		return err
	}
	provider, id, _ := strings.Cut(model, "/")
	for _, p := range catalog.All {
		if p.ID != provider {
			continue
		}
		if _, ok := p.Models[id]; !ok {
			return openCodeProblem(json.RawMessage(`{"name":"ModelNotFoundError"}`))
		}
		if catalog.Connected != nil {
			connected := false
			for _, value := range catalog.Connected {
				connected = connected || value == provider
			}
			if !connected {
				return openCodeProblem(json.RawMessage(`{"name":"ProviderAuthError"}`))
			}
		}
		input.model = model
		return nil
	}
	return openCodeProblem(json.RawMessage(`{"name":"ProviderModelNotFoundError"}`))
}

type openCodeWaitPolicy struct{ poll, firstText time.Duration }

func newOpenCodeMessageError(messages []openCodeMessage, previous string) error {
	for i := len(messages) - 1; i >= 0; i-- {
		info := messages[i].Info
		if info.ID == previous {
			break
		}
		if info.Role == "assistant" && len(info.Error) > 0 && string(info.Error) != "null" {
			return openCodeProblem(info.Error)
		}
		if info.Role == "assistant" {
			for _, part := range messages[i].Parts {
				if part.Type == "retry" {
					if _, err := retryDiagnostic("opencode", string(part.Error), part.Attempt, 0); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}
