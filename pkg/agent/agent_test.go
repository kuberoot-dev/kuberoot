package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	v1 "github.com/kuberoot-dev/kuberoot/pkg/apis/ai/v1alpha1"
)

func TestChoices(t *testing.T) {
	for subject, want := range map[string][]string{
		"service/n1/kubelet":  {v1.ActionRestartService, v1.ActionRebootNode, v1.ActionEscalate},
		"modelserver/n1/qwen": {v1.ActionRestartModelServer, v1.ActionRebootNode, v1.ActionEscalate},
		"node/n1":             {v1.ActionEscalate},
		"model/qwen":          {v1.ActionEscalate},
	} {
		if got := Choices(Finding{Subject: subject}); !slices.Equal(got, want) {
			t.Errorf("%s: %v, want %v", subject, got, want)
		}
	}
	if got := Choices(Finding{Subject: "model/qwen", Text: "… so it can be rolled back."}); !slices.Contains(got, v1.ActionRollbackModel) {
		t.Errorf("a model with a version before cannot be rolled back: %v", got)
	}
}

// model answers every request with content, and keeps the last request.
func model(t *testing.T, content string, got *map[string]any) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("asked %s", r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(got)
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": content}}}})
	}))
}

func TestDecideHoldsTheModelToTheChoices(t *testing.T) {
	var req map[string]any
	srv := model(t, `{"action":"RestartService","reason":"the log shows a lost connection"}`, &req)
	defer srv.Close()
	l := &LLM{Endpoint: srv.URL + "/v1", Model: "qwen"}
	f := Finding{Subject: "service/n1/kubelet", Node: "n1", Target: "kubelet", Text: "kubelet keeps restarting"}
	d, err := l.Decide(context.Background(), f)
	if err != nil || d.Action != v1.ActionRestartService {
		t.Fatalf("%+v %v", d, err)
	}
	schema := req["response_format"].(map[string]any)["json_schema"].(map[string]any)["schema"].(map[string]any)
	enum := schema["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]any)
	if len(enum) != 3 || enum[0] != v1.ActionRestartService {
		t.Fatalf("the request offered %v", enum)
	}
	// An answer outside the choices is not taken, whatever the endpoint sent.
	bad := model(t, `{"action":"RollbackModel","reason":"x"}`, &req)
	defer bad.Close()
	if _, err := (&LLM{Endpoint: bad.URL + "/v1", Model: "qwen"}).Decide(context.Background(), f); err == nil {
		t.Fatal("an action outside the choices was taken")
	}
	junk := model(t, `restart it`, &req)
	defer junk.Close()
	if _, err := (&LLM{Endpoint: junk.URL + "/v1", Model: "qwen"}).Decide(context.Background(), f); err == nil {
		t.Fatal("prose was taken for a decision")
	}
}
