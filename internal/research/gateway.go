package research

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// TextGateway is the minimum capability required by the research runner.
type TextGateway interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// GatewayConfig is runtime-only configuration. This package never reads API
// keys from environment variables, files, or process-global configuration.
type GatewayConfig struct {
	BaseURL          string
	Model            string
	APIKey           string
	Client           *http.Client
	Timeout          time.Duration
	MaxResponseBytes int64
	AllowLocal       bool
}

type OpenAICompatibleClient struct {
	endpoint         string
	model            string
	apiKey           string
	client           *http.Client
	timeout          time.Duration
	maxResponseBytes int64
}

func NewOpenAICompatibleClient(config GatewayConfig) (*OpenAICompatibleClient, error) {
	trimmedBaseURL := strings.TrimSpace(config.BaseURL)
	parsed, err := url.Parse(trimmedBaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("research: gateway base URL must be an absolute HTTP(S) URL")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("research: gateway base URL must not contain a query or fragment")
	}
	if parsed.User != nil {
		return nil, fmt.Errorf("research: gateway base URL user info is not allowed")
	}
	if err := validateHost(context.Background(), parsed.Hostname(), config.AllowLocal); err != nil {
		return nil, fmt.Errorf("research: gateway base URL: %w", err)
	}
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("research: gateway model must not be empty")
	}
	if config.Timeout <= 0 {
		config.Timeout = DefaultGatewayTimeout
	}
	if config.MaxResponseBytes <= 0 {
		config.MaxResponseBytes = DefaultMaxGatewayBytes
	}
	client := config.Client
	if client == nil {
		client = &http.Client{Transport: &http.Transport{Proxy: nil}}
	} else {
		copyOfClient := *client
		client = &copyOfClient
	}
	if client.Timeout <= 0 {
		client.Timeout = config.Timeout
	}
	endpoint, err := url.JoinPath(parsed.String(), "chat/completions")
	if err != nil {
		return nil, fmt.Errorf("research: gateway endpoint: %w", err)
	}
	origin := parsed.Scheme + "://" + parsed.Host
	maxRedirects := DefaultMaxRedirects
	client.CheckRedirect = func(next *http.Request, history []*http.Request) error {
		if len(history) > maxRedirects {
			return fmt.Errorf("research: gateway too many redirects")
		}
		nextOrigin := next.URL.Scheme + "://" + next.URL.Host
		if !strings.EqualFold(origin, nextOrigin) {
			return fmt.Errorf("research: gateway cross-origin redirect blocked")
		}
		return nil
	}
	return &OpenAICompatibleClient{
		endpoint:         endpoint,
		model:            config.Model,
		apiKey:           config.APIKey,
		client:           client,
		timeout:          config.Timeout,
		maxResponseBytes: config.MaxResponseBytes,
	}, nil
}

func (c *OpenAICompatibleClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestContext, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	payload := map[string]any{
		"model":       c.model,
		"temperature": 0,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userPrompt},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("research: encode gateway request: %w", err)
	}
	request, err := http.NewRequestWithContext(requestContext, http.MethodPost, c.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("research: create gateway request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return "", fmt.Errorf("research: gateway request: %w", err)
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, c.maxResponseBytes+1)
	responseBody, err := io.ReadAll(limited)
	if err != nil {
		return "", fmt.Errorf("research: read gateway response: %w", err)
	}
	if int64(len(responseBody)) > c.maxResponseBytes {
		return "", ErrGatewayResponseTooLarge
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("research: gateway returned HTTP %d", response.StatusCode)
	}
	text, err := extractGatewayText(responseBody)
	if err != nil {
		return "", err
	}
	return text, nil
}

func extractGatewayText(body []byte) (string, error) {
	var response struct {
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
				Refusal string          `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("research: decode gateway response: %w", err)
	}
	for _, choice := range response.Choices {
		if choice.Message.Refusal != "" {
			continue
		}
		var content string
		if json.Unmarshal(choice.Message.Content, &content) == nil && strings.TrimSpace(content) != "" {
			return content, nil
		}
		var parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(choice.Message.Content, &parts) == nil {
			var textParts []string
			for _, part := range parts {
				if part.Type == "text" && strings.TrimSpace(part.Text) != "" {
					textParts = append(textParts, part.Text)
				}
			}
			if len(textParts) > 0 {
				return strings.Join(textParts, "\n"), nil
			}
		}
	}
	return "", ErrNoTextCapability
}
