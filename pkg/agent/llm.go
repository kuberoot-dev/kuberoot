package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/ai/v1alpha1"
)

// Choices are the actions open for a finding: what can be done to its
// subject, and Escalate. The model picks among them; what they act on comes
// from the finding, never from the model.
func Choices(f Finding) []string {
	kind, _, _ := strings.Cut(f.Subject, "/")
	switch kind {
	case "service":
		return []string{v1.ActionRestartService, v1.ActionRebootNode, v1.ActionEscalate}
	case "modelserver":
		return []string{v1.ActionRestartModelServer, v1.ActionRebootNode, v1.ActionEscalate}
	case "model":
		if strings.Contains(f.Text, "can be rolled back") {
			return []string{v1.ActionRollbackModel, v1.ActionEscalate}
		}
	}
	return []string{v1.ActionEscalate}
}

// Decision is the model's answer for a finding.
type Decision struct {
	Action string `json:"action"`
	Reason string `json:"reason"`
}

const systemPrompt = `You operate a Kubernetes cluster that serves language models. You are shown one problem the cluster has, with the logs that go with it, and the actions you may take. Choose one.
Restarting helps a fault that passes: a crash, a hang, a lost connection. It does not help a wrong setting, a missing file, too little memory or disk, or weights that do not load; for those, and whenever you are unsure, choose Escalate, so a person looks.
Rebooting a node stops everything on it: choose it only when restarting the service alone cannot be enough.
Answer in JSON: the action, and the reason in one or two sentences that quote what in the problem made you choose it.`

// LLM asks an OpenAI-compatible endpoint.
type LLM struct {
	Endpoint string // up to and including /v1
	Model    string
	APIKey   string
	Client   *http.Client
}

// Decide asks the model what to do about a finding; the answer is held to
// the finding's choices by the request's schema, and checked again here.
func (l *LLM) Decide(ctx context.Context, f Finding) (Decision, error) {
	choices := Choices(f)
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{"type": "string", "enum": choices},
			"reason": map[string]any{"type": "string", "maxLength": 600},
		},
		"required":             []string{"action", "reason"},
		"additionalProperties": false,
	}
	body, _ := json.Marshal(map[string]any{
		"model": l.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": "Problem:\n" + f.Text + "\n\nActions you may take: " + strings.Join(choices, ", ") + "."},
		},
		"temperature": 0,
		"max_tokens":  300,
		"response_format": map[string]any{"type": "json_schema",
			"json_schema": map[string]any{"name": "decision", "strict": true, "schema": schema}},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(l.Endpoint, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return Decision{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if l.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+l.APIKey)
	}
	client := l.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Decision{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return Decision{}, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Choices) == 0 {
		return Decision{}, fmt.Errorf("an answer that is not a completion: %.200s", raw)
	}
	var d Decision
	if err := json.Unmarshal([]byte(out.Choices[0].Message.Content), &d); err != nil {
		return Decision{}, fmt.Errorf("an answer that is not the decision asked for: %.200s", out.Choices[0].Message.Content)
	}
	for _, c := range choices {
		if d.Action == c {
			return d, nil
		}
	}
	return Decision{}, fmt.Errorf("the model chose %q, not one of %v", d.Action, choices)
}
