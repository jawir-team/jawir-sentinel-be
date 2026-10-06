// Package embedding creates Vertex AI embeddings and atomically persists policy
// indexes without keeping database transactions open during network calls.
package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	EmbeddingModel     = "gemini-embedding-001"
	EmbeddingDimension = 768
	TaskTypeDocument   = "RETRIEVAL_DOCUMENT"
	TaskTypeQuery      = "RETRIEVAL_QUERY"

	maxVertexResponseBytes = 10 << 20
)

// Embedder generates document embeddings in the same order as texts.
type Embedder interface {
	EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error)
}

// VertexEmbedder calls the Vertex AI publisher-model predict endpoint.
type VertexEmbedder struct {
	accessToken string
	httpClient  *http.Client
	endpoint    string
}

// NewVertexEmbedder constructs a Vertex AI REST client. Production wiring must
// supply accessToken from Secret Manager or workload identity; this package
// deliberately contains no fake credential or authentication bypass.
func NewVertexEmbedder(project, location, accessToken string, httpClient *http.Client) (*VertexEmbedder, error) {
	if strings.TrimSpace(project) == "" {
		return nil, errors.New("Vertex AI project is required")
	}
	if strings.TrimSpace(location) == "" {
		return nil, errors.New("Vertex AI location is required")
	}
	if strings.TrimSpace(accessToken) == "" {
		return nil, errors.New("Vertex AI access token is required")
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}

	endpoint := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:predict",
		location,
		url.PathEscape(project),
		url.PathEscape(location),
		EmbeddingModel,
	)
	return &VertexEmbedder{
		accessToken: accessToken,
		httpClient:  httpClient,
		endpoint:    endpoint,
	}, nil
}

type vertexPredictRequest struct {
	Instances  []vertexInstance `json:"instances"`
	Parameters vertexParameters `json:"parameters"`
}

type vertexInstance struct {
	TaskType string        `json:"task_type"`
	Content  vertexContent `json:"content"`
}

type vertexContent struct {
	Parts []vertexPart `json:"parts"`
}

type vertexPart struct {
	Text string `json:"text"`
}

type vertexParameters struct {
	OutputDimensionality int  `json:"outputDimensionality"`
	AutoTruncate         bool `json:"autoTruncate"`
}

type vertexPredictResponse struct {
	Predictions []struct {
		Embeddings struct {
			Values []float32 `json:"values"`
		} `json:"embeddings"`
	} `json:"predictions"`
}

// EmbedDocuments requests retrieval-document embeddings from Vertex AI.
func (e *VertexEmbedder) EmbedDocuments(ctx context.Context, texts []string) ([][]float32, error) {
	instances := make([]vertexInstance, len(texts))
	for i, text := range texts {
		instances[i] = vertexInstance{
			TaskType: TaskTypeDocument,
			Content:  vertexContent{Parts: []vertexPart{{Text: text}}},
		}
	}
	payload, err := json.Marshal(vertexPredictRequest{
		Instances: instances,
		Parameters: vertexParameters{
			OutputDimensionality: EmbeddingDimension,
			AutoTruncate:         true,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("encode Vertex AI embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("create Vertex AI embedding request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.accessToken)
	req.Header.Set("Content-Type", "application/json")

	response, err := e.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call Vertex AI embedding endpoint: %w", err)
	}
	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, readErr := io.ReadAll(io.LimitReader(response.Body, maxVertexResponseBytes))
		if readErr != nil {
			return nil, fmt.Errorf("Vertex AI embedding endpoint returned %s (read response: %v)", response.Status, readErr)
		}
		return nil, fmt.Errorf("Vertex AI embedding endpoint returned %s: %s", response.Status, strings.TrimSpace(string(body)))
	}

	var decoded vertexPredictResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, maxVertexResponseBytes)).Decode(&decoded); err != nil {
		return nil, fmt.Errorf("decode Vertex AI embedding response: %w", err)
	}
	if len(decoded.Predictions) != len(texts) {
		return nil, fmt.Errorf("Vertex AI returned %d predictions for %d texts", len(decoded.Predictions), len(texts))
	}

	vectors := make([][]float32, len(decoded.Predictions))
	for i, prediction := range decoded.Predictions {
		if len(prediction.Embeddings.Values) != EmbeddingDimension {
			return nil, fmt.Errorf(
				"Vertex AI prediction %d has dimension %d, want %d",
				i,
				len(prediction.Embeddings.Values),
				EmbeddingDimension,
			)
		}
		vectors[i] = prediction.Embeddings.Values
	}
	return vectors, nil
}
