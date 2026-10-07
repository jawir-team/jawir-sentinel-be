package vertexai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

func TestGenerateContentTextRequest(t *testing.T) {
	server := newInMemoryHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/gemini-test:generateContent") {
			t.Errorf("path = %q, want generateContent endpoint", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q", got)
		}
		var payload struct {
			Contents []struct {
				Role  string `json:"role"`
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if len(payload.Contents) != 1 || payload.Contents[0].Role != "user" {
			t.Fatalf("contents = %#v", payload.Contents)
		}
		if got := payload.Contents[0].Parts; len(got) != 2 || got[0].Text != "first" || got[1].Text != "second" {
			t.Errorf("parts = %#v", got)
		}
		writeGeneratedText(t, w, `{"status":"PASS"}`)
	}))
	defer server.Close()

	client := newTestClient(t, server, 0, nil)
	response, err := client.GenerateContent(context.Background(), Request{TextParts: []string{"first", "second"}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if response.Text != `{"status":"PASS"}` || response.Verdict != VerdictPass {
		t.Errorf("response = %#v", response)
	}
	if response.Attempts != 1 || response.Duration <= 0 {
		t.Errorf("telemetry = attempts %d, duration %s", response.Attempts, response.Duration)
	}
}

func TestGenerateContentFileParts(t *testing.T) {
	want := []FileInput{
		{URI: "gs://validated-bucket/document.pdf", MIMEType: "application/pdf"},
		{URI: "gs://validated-bucket/photo.jpg", MIMEType: "image/jpeg"},
		{URI: "gs://validated-bucket/image.png", MIMEType: "image/png"},
	}
	server := newInMemoryHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Contents []struct {
				Parts []struct {
					FileData *struct {
						FileURI  string `json:"fileUri"`
						MIMEType string `json:"mimeType"`
					} `json:"fileData"`
				} `json:"parts"`
			} `json:"contents"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
			return
		}
		if len(payload.Contents) != 1 || len(payload.Contents[0].Parts) != len(want) {
			t.Fatalf("payload = %#v", payload)
		}
		for i, part := range payload.Contents[0].Parts {
			if part.FileData == nil || part.FileData.FileURI != want[i].URI || part.FileData.MIMEType != want[i].MIMEType {
				t.Errorf("part %d = %#v, want %#v", i, part.FileData, want[i])
			}
		}
		writeGeneratedText(t, w, "analysis complete")
	}))
	defer server.Close()

	response, err := newTestClient(t, server, 0, nil).GenerateContent(context.Background(), Request{FileParts: want})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if response.Verdict != VerdictUnknown {
		t.Errorf("Verdict = %q, want unknown", response.Verdict)
	}
}

func TestGenerateContentValidatesInput(t *testing.T) {
	tests := []struct {
		name    string
		request Request
	}{
		{name: "empty", request: Request{}},
		{name: "empty text", request: Request{TextParts: []string{"  "}}},
		{name: "HTTP URI", request: Request{FileParts: []FileInput{{URI: "https://example.com/evidence.pdf", MIMEType: "application/pdf"}}}},
		{name: "relative URI", request: Request{FileParts: []FileInput{{URI: "evidence.pdf", MIMEType: "application/pdf"}}}},
		{name: "missing object", request: Request{FileParts: []FileInput{{URI: "gs://bucket", MIMEType: "application/pdf"}}}},
		{name: "URI query", request: Request{FileParts: []FileInput{{URI: "gs://bucket/evidence.pdf?generation=1", MIMEType: "application/pdf"}}}},
		{name: "unsupported MIME", request: Request{FileParts: []FileInput{{URI: "gs://bucket/evidence.gif", MIMEType: "image/gif"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, nil, 0, nil)
			response, err := client.GenerateContent(context.Background(), test.request)
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v, want ErrInvalidRequest", err)
			}
			if response.Attempts != 0 {
				t.Errorf("attempts = %d, want 0", response.Attempts)
			}
		})
	}
}

func TestGenerateContentRetriesServerFailures(t *testing.T) {
	var attempts atomic.Int32
	server := newInMemoryHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempt := attempts.Add(1)
		if attempt <= 2 {
			http.Error(w, "temporary", http.StatusInternalServerError)
			return
		}
		writeGeneratedText(t, w, `{"status":"PASS_WITH_WARNING"}`)
	}))
	defer server.Close()

	client := newTestClient(t, server, 2, nil)
	response, err := client.GenerateContent(context.Background(), Request{TextParts: []string{"safe input"}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if attempts.Load() != 3 || response.Attempts != 3 {
		t.Errorf("attempts = server %d / response %d, want 3", attempts.Load(), response.Attempts)
	}
	if response.Verdict != VerdictPassWithWarning {
		t.Errorf("Verdict = %q", response.Verdict)
	}
}

func TestGenerateContentRetriesRateLimit(t *testing.T) {
	var attempts atomic.Int32
	server := newInMemoryHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "rate limited", http.StatusTooManyRequests)
			return
		}
		writeGeneratedText(t, w, `{"status":"PASS"}`)
	}))
	defer server.Close()

	response, err := newTestClient(t, server, 1, nil).GenerateContent(context.Background(), Request{TextParts: []string{"input"}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if attempts.Load() != 2 || response.Attempts != 2 {
		t.Errorf("attempts = server %d / response %d, want 2", attempts.Load(), response.Attempts)
	}
}

func TestGenerateContentRetriesTransportFailure(t *testing.T) {
	var attempts atomic.Int32
	httpClient := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		if attempts.Add(1) <= 2 {
			return nil, errors.New("temporary transport failure")
		}
		body := `{"candidates":[{"content":{"parts":[{"text":"{\"status\":\"PASS\"}"}]}}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})}
	client, err := New(Config{
		ProjectID:      "project",
		Location:       "asia-southeast1",
		Model:          "gemini-test",
		EmbeddingModel: "embedding-test",
		MaxRetries:     2,
	}, "test-token", httpClient, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	client.backoffBase = 0

	response, err := client.GenerateContent(context.Background(), Request{TextParts: []string{"input"}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if attempts.Load() != 3 || response.Attempts != 3 || response.Verdict != VerdictPass {
		t.Errorf("attempts/verdict = %d/%d/%q", attempts.Load(), response.Attempts, response.Verdict)
	}
}

func TestGenerateContentDoesNotRetryBadRequest(t *testing.T) {
	var attempts atomic.Int32
	server := newInMemoryHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "invalid", http.StatusBadRequest)
	}))
	defer server.Close()

	response, err := newTestClient(t, server, 5, nil).GenerateContent(context.Background(), Request{TextParts: []string{"input"}})
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if attempts.Load() != 1 || response.Attempts != 1 {
		t.Errorf("attempts = server %d / response %d, want 1", attempts.Load(), response.Attempts)
	}
}

func TestGenerateContentRetryLimitAndErrorTranslation(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		maxRetries int
		wantErr    error
		wantCalls  int32
	}{
		{name: "quota exhausted", status: http.StatusTooManyRequests, maxRetries: 2, wantErr: ErrQuotaExhausted, wantCalls: 3},
		{name: "unavailable", status: http.StatusServiceUnavailable, maxRetries: 2, wantErr: ErrUpstreamUnavailable, wantCalls: 3},
		{name: "zero retries", status: http.StatusInternalServerError, maxRetries: 0, wantErr: ErrUpstreamUnavailable, wantCalls: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := newInMemoryHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				http.Error(w, "provider error", test.status)
			}))
			defer server.Close()

			response, err := newTestClient(t, server, test.maxRetries, nil).GenerateContent(context.Background(), Request{TextParts: []string{"input"}})
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("error = %v, want %v", err, test.wantErr)
			}
			if calls.Load() != test.wantCalls || response.Attempts != int(test.wantCalls) {
				t.Errorf("attempts = server %d / response %d, want %d", calls.Load(), response.Attempts, test.wantCalls)
			}
		})
	}
}

func TestGenerateContentDecodeFailureIsNotRetried(t *testing.T) {
	var calls atomic.Int32
	server := newInMemoryHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte("not JSON"))
	}))
	defer server.Close()

	response, err := newTestClient(t, server, 4, nil).GenerateContent(context.Background(), Request{TextParts: []string{"input"}})
	if !errors.Is(err, ErrDecodeFailed) {
		t.Fatalf("error = %v, want ErrDecodeFailed", err)
	}
	if calls.Load() != 1 || response.Attempts != 1 {
		t.Errorf("attempts = server %d / response %d, want 1", calls.Load(), response.Attempts)
	}
}

func TestVerifierFailIsSemanticResult(t *testing.T) {
	server := newInMemoryHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeGeneratedText(t, w, "```json\n{\"status\":\"FAIL\",\"issues\":[]}\n```")
	}))
	defer server.Close()

	response, err := newTestClient(t, server, 3, nil).GenerateContent(context.Background(), Request{TextParts: []string{"verify"}})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if response.Verdict != VerdictFail {
		t.Errorf("Verdict = %q, want FAIL", response.Verdict)
	}
	if response.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", response.Attempts)
	}
}

func TestConfigFromEnv(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("AI_TECHNICAL_MAX_RETRIES", "7")
	config, err := ConfigFromEnv()
	if err != nil {
		t.Fatalf("ConfigFromEnv() error = %v", err)
	}
	if config.ProjectID != "project" || config.Location != "asia-southeast1" || config.Model != "gemini-test" || config.EmbeddingModel != "embedding-test" || config.MaxRetries != 7 {
		t.Errorf("config = %#v", config)
	}

	t.Run("invalid retries use default", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("AI_TECHNICAL_MAX_RETRIES", "invalid")
		config, err := ConfigFromEnv()
		if err != nil {
			t.Fatalf("ConfigFromEnv() error = %v", err)
		}
		if config.MaxRetries != DefaultTechnicalMaxRetries {
			t.Errorf("MaxRetries = %d, want %d", config.MaxRetries, DefaultTechnicalMaxRetries)
		}
	})

	t.Run("retry range is clamped", func(t *testing.T) {
		setRequiredEnv(t)
		t.Setenv("AI_TECHNICAL_MAX_RETRIES", "99")
		config, err := ConfigFromEnv()
		if err != nil {
			t.Fatalf("ConfigFromEnv() error = %v", err)
		}
		if config.MaxRetries != MaxTechnicalMaxRetries {
			t.Errorf("MaxRetries = %d, want %d", config.MaxRetries, MaxTechnicalMaxRetries)
		}
	})
}

func TestConfigFromEnvRejectsMissingRequiredValues(t *testing.T) {
	variables := []string{"GCP_PROJECT_ID", "VERTEX_AI_LOCATION", "VERTEX_AI_MODEL"}
	for _, missing := range variables {
		t.Run(missing, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(missing, "")
			_, err := ConfigFromEnv()
			if !errors.Is(err, ErrInvalidConfig) || !strings.Contains(err.Error(), missing) {
				t.Fatalf("error = %v, want missing %s", err, missing)
			}
		})
	}
}

func TestNewFromEnvAndCredentialValidation(t *testing.T) {
	setRequiredEnv(t)
	client, err := NewFromEnv("token", nil, nil)
	if err != nil {
		t.Fatalf("NewFromEnv() error = %v", err)
	}
	if client.Config().EmbeddingModel != "embedding-test" {
		t.Errorf("EmbeddingModel = %q", client.Config().EmbeddingModel)
	}
	if _, err := NewFromEnv("", nil, nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("missing token error = %v, want ErrInvalidConfig", err)
	}
}

func TestGenerateContentLogsMetadataWithoutRawContent(t *testing.T) {
	const (
		rawText = "highly-sensitive-policy-and-evidence"
		rawURI  = "gs://bucket/private-evidence.pdf"
	)
	var logs bytes.Buffer
	logger := logging.New(&logs, slog.LevelInfo)
	server := newInMemoryHTTPTestServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeGeneratedText(t, w, `{"status":"PASS"}`)
	}))
	defer server.Close()

	_, err := newTestClient(t, server, 0, logger).GenerateContent(context.Background(), Request{
		TextParts: []string{rawText},
		FileParts: []FileInput{{URI: rawURI, MIMEType: "application/pdf"}},
	})
	if err != nil {
		t.Fatalf("GenerateContent() error = %v", err)
	}
	if strings.Contains(logs.String(), rawText) || strings.Contains(logs.String(), rawURI) || strings.Contains(logs.String(), "private-evidence.pdf") {
		t.Fatalf("raw content leaked in logs: %s", logs.String())
	}
	for _, metadata := range []string{"gemini-test", "application/pdf", "success", `"attempt":1`} {
		if !strings.Contains(logs.String(), metadata) {
			t.Errorf("logs missing metadata %q: %s", metadata, logs.String())
		}
	}
}

func newTestClient(t *testing.T, server *httptest.Server, maxRetries int, logger *slog.Logger) *Client {
	t.Helper()
	var httpClient *http.Client
	if server != nil {
		httpClient = server.Client()
	} else {
		httpClient = &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			t.Fatal("unexpected HTTP request")
			return nil, errors.New("unexpected HTTP request")
		})}
	}
	client, err := New(Config{
		ProjectID:      "project",
		Location:       "asia-southeast1",
		Model:          "gemini-test",
		EmbeddingModel: "embedding-test",
		MaxRetries:     maxRetries,
	}, "test-token", httpClient, logger)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if server != nil {
		client.endpoint = server.URL + "/v1/projects/project/locations/asia-southeast1/publishers/google/models/gemini-test:generateContent"
	}
	client.backoffBase = 0
	return client
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GCP_PROJECT_ID", "project")
	t.Setenv("VERTEX_AI_LOCATION", "asia-southeast1")
	t.Setenv("VERTEX_AI_MODEL", "gemini-test")
	t.Setenv("VERTEX_EMBEDDING_MODEL", "embedding-test")
	t.Setenv("AI_TECHNICAL_MAX_RETRIES", "")
}

func writeGeneratedText(t *testing.T, w http.ResponseWriter, text string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{
				"role":  "model",
				"parts": []any{map[string]any{"text": text}},
			},
		}},
	}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

// newInMemoryHTTPTestServer uses httptest.Server over net.Pipe so the tests do
// not require a TCP listener (and remain hermetic in network-disabled sandboxes).
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

func (l *pipeListener) Addr() net.Addr { return pipeAddr("in-memory.test") }

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
	_ net.Listener      = (*pipeListener)(nil)
	_ net.Addr          = pipeAddr("")
	_ http.RoundTripper = roundTripperFunc(nil)
)
