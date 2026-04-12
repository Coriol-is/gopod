// Package memory provides picoclaw's long-term memory layer: a hybrid
// FTS5 + sqlite-vec store backed by OpenAI embeddings.
//
// Design: docs/MEMORY.md
// Decisions: D005 (5-layer model), D016 (embedding model + dim)
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

// Embedder turns text into float32 vectors for sqlite-vec storage
// and retrieval. Implementations must return vectors whose
// dimensionality matches the vec0 schema (1024 per D016).
type Embedder interface {
	Model() string
	Dim() int
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// OpenAIEmbedder calls OpenAI's /v1/embeddings endpoint with the
// `dimensions` parameter per D016.
type OpenAIEmbedder struct {
	apiKey string
	model  string
	dim    int
	client *http.Client
}

// NewOpenAIEmbedder creates an embedder using text-embedding-3-small
// at 1024 dimensions. apiKey is read from os.Getenv("OPENAI_API_KEY")
// if empty.
func NewOpenAIEmbedder(apiKey, model string, dim int) (*OpenAIEmbedder, error) {
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("memory: OPENAI_API_KEY is not set")
	}
	if model == "" {
		model = "text-embedding-3-small"
	}
	if dim <= 0 {
		dim = 1024
	}
	return &OpenAIEmbedder{
		apiKey: apiKey,
		model:  model,
		dim:    dim,
		client: &http.Client{},
	}, nil
}

func (e *OpenAIEmbedder) Model() string { return e.model }
func (e *OpenAIEmbedder) Dim() int      { return e.dim }

// Embed calls the OpenAI embeddings API. Supports batching (up to
// 2048 inputs per call per OpenAI docs).
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}

	reqBody := embeddingRequest{
		Model:      e.model,
		Input:      texts,
		Dimensions: e.dim,
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("memory: marshal embed request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.openai.com/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("memory: create embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("memory: embed request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("memory: read embed response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("memory: OpenAI embeddings API returned %d: %s",
			resp.StatusCode, truncateBytes(respBody, 200))
	}

	var result embeddingResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("memory: parse embed response: %w", err)
	}

	vectors := make([][]float32, len(result.Data))
	for i, d := range result.Data {
		vectors[i] = d.Embedding
	}
	return vectors, nil
}

type embeddingRequest struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
		Index     int       `json:"index"`
	} `json:"data"`
	Usage struct {
		TotalTokens int `json:"total_tokens"`
	} `json:"usage"`
}

func truncateBytes(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}
