package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	geminiBaseURL    = "https://generativelanguage.googleapis.com/v1beta/models"
	defaultModel     = "gemini-2.5-flash-lite"
	defaultTimeout   = 30 * time.Second
	rateLimitInterval = 4 * time.Second // 15 RPM
)

// GeminiClient is a lightweight REST client for Google Gemini API.
// Returns nil from NewGeminiClient if GEMINI_API_KEY is not set.
type GeminiClient struct {
	apiKey     string
	model      string
	httpClient *http.Client
	lastCall   time.Time
	mu         sync.Mutex
}

// NewGeminiClient creates a Gemini client from GEMINI_API_KEY env var.
// Returns nil if the key is not set (AI features disabled).
func NewGeminiClient() *GeminiClient {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		return nil
	}
	return &GeminiClient{
		apiKey: key,
		model:  defaultModel,
		httpClient: &http.Client{
			Timeout: defaultTimeout,
		},
	}
}

// geminiRequest is the API request body
type geminiRequest struct {
	Contents         []geminiContent  `json:"contents"`
	GenerationConfig *geminiGenConfig `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text string `json:"text"`
}

type geminiGenConfig struct {
	MaxOutputTokens int     `json:"maxOutputTokens,omitempty"`
	Temperature     float64 `json:"temperature,omitempty"`
}

// geminiResponse is the API response body
type geminiResponse struct {
	Candidates []struct {
		Content struct {
			Parts []geminiPart `json:"parts"`
		} `json:"content"`
	} `json:"candidates"`
	Error *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// Generate sends a prompt to Gemini and returns the text response.
// On any error, returns empty string and the error (caller should handle gracefully).
func (c *GeminiClient) Generate(ctx context.Context, prompt string, maxTokens int) (string, error) {
	if c == nil {
		return "", nil
	}

	// Rate limiting
	c.mu.Lock()
	elapsed := time.Since(c.lastCall)
	if elapsed < rateLimitInterval {
		wait := rateLimitInterval - elapsed
		c.mu.Unlock()
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return "", ctx.Err()
		}
		c.mu.Lock()
	}
	c.lastCall = time.Now()
	c.mu.Unlock()

	// Build request
	reqBody := geminiRequest{
		Contents: []geminiContent{
			{Parts: []geminiPart{{Text: prompt}}},
		},
	}
	if maxTokens > 0 {
		reqBody.GenerationConfig = &geminiGenConfig{
			MaxOutputTokens: maxTokens,
			Temperature:     0.3, // low temperature for consistent analytical output
		}
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/%s:generateContent", geminiBaseURL, c.model)
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", c.apiKey)

	// Execute with retry on 429
	var resp *http.Response
	for attempt := 0; attempt < 2; attempt++ {
		resp, err = c.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("http request: %w", err)
		}

		if resp.StatusCode == 429 {
			resp.Body.Close()
			backoff := time.Duration(5*(attempt+1)) * time.Second
			log.Printf("[AI] Rate limited (429), backing off %s", backoff)
			select {
			case <-time.After(backoff):
				// Rebuild request for retry
				req, _ = http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("x-goog-api-key", c.apiKey)
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		break
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("gemini API error %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var gemResp geminiResponse
	if err := json.NewDecoder(resp.Body).Decode(&gemResp); err != nil {
		return "", fmt.Errorf("decode response: %w", err)
	}

	if gemResp.Error != nil {
		return "", fmt.Errorf("gemini error %d: %s", gemResp.Error.Code, gemResp.Error.Message)
	}

	if len(gemResp.Candidates) == 0 || len(gemResp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("empty response from gemini")
	}

	return gemResp.Candidates[0].Content.Parts[0].Text, nil
}
