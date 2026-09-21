package research

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOpenAICompatibleGatewayAndResearchResult(t *testing.T) {
	snapshot := NewSourceSnapshot("https://source.example/article", "text/plain", []byte("A verified observation."), timeNow())
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/chat/completions" {
			t.Fatalf("path = %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer runtime-key" {
			t.Fatalf("authorization header was not set")
		}
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.Model != "research-model" {
			t.Fatalf("request payload = %#v, err = %v", payload, err)
		}
		citation := Citation{SourceURL: snapshot.SourceURL, ContentHash: snapshot.ContentHash, Quote: "A verified observation."}
		result := EvidenceBundle{
			Evidence: []Evidence{{Statement: "Observation is supported.", Citations: []Citation{citation}}},
		}
		response := map[string]any{"choices": []any{map[string]any{"message": map[string]any{"content": mustJSON(result)}}}}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(response)
	}))
	defer server.Close()
	client, err := NewOpenAICompatibleClient(GatewayConfig{
		BaseURL:    "  " + server.URL + "/v1/  ",
		Model:      "research-model",
		APIKey:     "runtime-key",
		AllowLocal: true,
	})
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	got, err := Conduct(context.Background(), client, "What does the source support?", []SourceSnapshot{snapshot})
	if err != nil {
		t.Fatalf("Conduct() error = %v", err)
	}
	if len(got.Evidence) != 1 || got.Evidence[0].Citations[0].Quote != "A verified observation." {
		t.Fatalf("result = %#v", got)
	}
	if got.Topic != "What does the source support?" || len(got.Sources) != 1 {
		t.Fatalf("bundle contract = %#v", got)
	}
}

func TestGatewayRejectsNoTextCapabilityAndOversize(t *testing.T) {
	noTextServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"tool_calls":[{"id":"1"}]}}]}`))
	}))
	defer noTextServer.Close()
	client, err := NewOpenAICompatibleClient(GatewayConfig{BaseURL: noTextServer.URL, Model: "m", AllowLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), "system", "user"); !errors.Is(err, ErrNoTextCapability) {
		t.Fatalf("no-text error = %v", err)
	}

	largeServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(strings.Repeat("x", 20)))
	}))
	defer largeServer.Close()
	client, err = NewOpenAICompatibleClient(GatewayConfig{BaseURL: largeServer.URL, Model: "m", MaxResponseBytes: 5, AllowLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), "system", "user"); !errors.Is(err, ErrGatewayResponseTooLarge) {
		t.Fatalf("gateway size error = %v", err)
	}
}

func TestResearchResultRejectsUncitedClaim(t *testing.T) {
	snapshot := NewSourceSnapshot("https://source.example/article", "text/plain", []byte("supported"), timeNow())
	result := ResearchResult{
		Evidence: []Evidence{{Statement: "unsupported", Citations: nil}},
		Topic:    "question",
	}
	if !errors.Is(result.Validate([]SourceSnapshot{snapshot}), ErrNoCitations) {
		t.Fatalf("Validate() error = %v", result.Validate([]SourceSnapshot{snapshot}))
	}
}

func TestResearchPromptAndResultHaveNoJudgmentBoundaryFields(t *testing.T) {
	snapshot := NewSourceSnapshot("https://source.example/article", "text/plain", []byte("A sourced fact."), timeNow())
	var gateway promptRecordingGateway
	gateway.response = mustJSON(EvidenceBundle{
		Evidence: []Evidence{{Statement: "Fact is sourced.", Citations: []Citation{{SourceURL: snapshot.SourceURL, ContentHash: snapshot.ContentHash, Quote: "A sourced fact."}}}},
	})
	if _, err := Conduct(context.Background(), &gateway, "Research only", []SourceSnapshot{snapshot}); err != nil {
		t.Fatalf("Conduct() error = %v", err)
	}
	for _, prompt := range []string{gateway.system, gateway.user} {
		for _, forbidden := range []string{"\"judgment\"", "\"thesis\"", "atom_drafts"} {
			if strings.Contains(strings.ToLower(prompt), strings.ToLower(forbidden)) {
				t.Fatalf("research prompt contains downstream field %q: %s", forbidden, prompt)
			}
		}
	}
}

func TestGatewayRejectsCrossOriginRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Location", "http://other.example/chat/completions")
		writer.WriteHeader(http.StatusFound)
	}))
	defer target.Close()
	client, err := NewOpenAICompatibleClient(GatewayConfig{BaseURL: target.URL, Model: "m", AllowLocal: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Complete(context.Background(), "system", "user"); err == nil || !strings.Contains(err.Error(), "cross-origin redirect blocked") {
		t.Fatalf("redirect error = %v", err)
	}
}

func TestResearchRejectsEmptyResultAndInputBudget(t *testing.T) {
	snapshot := NewSourceSnapshot("https://source.example/article", "text/plain", []byte("large source text"), timeNow())
	if !errors.Is((ResearchResult{}).Validate([]SourceSnapshot{snapshot}), ErrInvalidResult) {
		t.Fatalf("empty result should fail minimum schema")
	}
	large := NewSourceSnapshot("https://source.example/large", "text/plain", []byte(strings.Repeat("x", 32)), timeNow())
	var gateway recordingGateway
	_, err := ConductWithConfig(context.Background(), gateway, "question", []SourceSnapshot{large}, ResearchConfig{MaxInputBytes: 8})
	if !errors.Is(err, ErrResearchInputTooLarge) {
		t.Fatalf("input budget error = %v", err)
	}
}

type recordingGateway struct{}

func (recordingGateway) Complete(context.Context, string, string) (string, error) {
	return "{}", nil
}

type promptRecordingGateway struct {
	system   string
	user     string
	response string
}

func (g *promptRecordingGateway) Complete(_ context.Context, systemPrompt, userPrompt string) (string, error) {
	g.system = systemPrompt
	g.user = userPrompt
	return g.response, nil
}

func mustJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func timeNow() (now time.Time) {
	return time.Now().UTC()
}
