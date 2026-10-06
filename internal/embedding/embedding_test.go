package embedding

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestVertexEmbedderRequestAndResponse(t *testing.T) {
	vector := make([]float32, EmbeddingDimension)
	for i := range vector {
		vector[i] = float32(i) / 10
	}

	server := newInMemoryHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.Contains(r.URL.Path, EmbeddingModel+":predict") {
			t.Errorf("path = %q, want model predict path", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}

		var request struct {
			Instances []struct {
				TaskType string `json:"task_type"`
				Content  struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
			} `json:"instances"`
			Parameters struct {
				OutputDimensionality int  `json:"outputDimensionality"`
				AutoTruncate         bool `json:"autoTruncate"`
			} `json:"parameters"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(request.Instances) != 1 || request.Instances[0].TaskType != TaskTypeDocument {
			t.Errorf("instances = %+v", request.Instances)
		}
		if len(request.Instances) == 1 && (len(request.Instances[0].Content.Parts) != 1 || request.Instances[0].Content.Parts[0].Text != "policy text") {
			t.Errorf("content parts = %+v", request.Instances[0].Content.Parts)
		}
		if request.Parameters.OutputDimensionality != EmbeddingDimension || !request.Parameters.AutoTruncate {
			t.Errorf("parameters = %+v", request.Parameters)
		}

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"predictions": []any{map[string]any{
				"embeddings": map[string]any{"values": vector},
			}},
		}); err != nil {
			t.Errorf("encode response: %v", err)
		}
	}))
	defer server.Close()

	embedder, err := NewVertexEmbedder("project", "asia-southeast1", "test-token", server.Client())
	if err != nil {
		t.Fatalf("NewVertexEmbedder() error = %v", err)
	}
	embedder.endpoint = server.URL + "/v1/projects/project/locations/asia-southeast1/publishers/google/models/" + EmbeddingModel + ":predict"

	got, err := embedder.EmbedDocuments(context.Background(), []string{"policy text"})
	if err != nil {
		t.Fatalf("EmbedDocuments() error = %v", err)
	}
	if len(got) != 1 || len(got[0]) != EmbeddingDimension {
		t.Fatalf("dimensions = %d x %d", len(got), len(got[0]))
	}
	if got[0][17] != vector[17] {
		t.Errorf("vector[17] = %v, want %v", got[0][17], vector[17])
	}
}

func TestVertexEmbedderRejectsWrongDimension(t *testing.T) {
	server := newInMemoryHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"predictions": []any{map[string]any{
				"embeddings": map[string]any{"values": []float32{1, 2, 3}},
			}},
		})
	}))
	defer server.Close()

	embedder, err := NewVertexEmbedder("project", "location", "token", server.Client())
	if err != nil {
		t.Fatalf("NewVertexEmbedder() error = %v", err)
	}
	embedder.endpoint = server.URL

	_, err = embedder.EmbedDocuments(context.Background(), []string{"text"})
	if err == nil || !strings.Contains(err.Error(), "dimension 3") {
		t.Fatalf("EmbedDocuments() error = %v, want dimension error", err)
	}
}

func TestNewVertexEmbedderRejectsMissingConfiguration(t *testing.T) {
	tests := []struct {
		name        string
		project     string
		location    string
		accessToken string
	}{
		{name: "project", project: "", location: "location", accessToken: "token"},
		{name: "location", project: "project", location: "", accessToken: "token"},
		{name: "token", project: "project", location: "location", accessToken: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewVertexEmbedder(test.project, test.location, test.accessToken, nil); err == nil {
				t.Fatal("NewVertexEmbedder() error = nil")
			}
		})
	}
}

// newInMemoryHTTPTestServer uses httptest.Server over net.Pipe so the tests do
// not require a TCP listener (and can run in network-disabled sandboxes).
func newInMemoryHTTPTestServer(handler http.Handler) *httptest.Server {
	listener := &pipeListener{
		connections: make(chan net.Conn),
		closed:      make(chan struct{}),
	}
	server := &httptest.Server{
		Listener: listener,
		Config:   &http.Server{Handler: handler},
	}
	server.Start()
	server.Client().Transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return listener.dial(ctx)
		},
	}
	return server
}

type pipeListener struct {
	connections chan net.Conn
	closed      chan struct{}
	closeOnce   sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case connection := <-l.connections:
		return connection, nil
	case <-l.closed:
		return nil, net.ErrClosed
	}
}

func (l *pipeListener) Close() error {
	l.closeOnce.Do(func() { close(l.closed) })
	return nil
}

func (l *pipeListener) Addr() net.Addr {
	return pipeAddr("in-memory.test")
}

func (l *pipeListener) dial(ctx context.Context) (net.Conn, error) {
	client, server := net.Pipe()
	select {
	case l.connections <- server:
		return client, nil
	case <-ctx.Done():
		_ = client.Close()
		_ = server.Close()
		return nil, ctx.Err()
	case <-l.closed:
		_ = client.Close()
		_ = server.Close()
		return nil, net.ErrClosed
	}
}

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

var (
	_ net.Listener = (*pipeListener)(nil)
	_ net.Addr     = pipeAddr("")
)
