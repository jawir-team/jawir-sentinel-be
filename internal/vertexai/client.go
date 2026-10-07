// Package vertexai provides a reusable, workflow-independent REST client for
// Gemini generation on Vertex AI.
package vertexai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jawir-team/jawir-sentinel-be/internal/logging"
)

const (
	DefaultTechnicalMaxRetries = 3
	MaxTechnicalMaxRetries     = 10

	maxVertexResponseBytes = 10 << 20
	defaultBackoff         = 200 * time.Millisecond
	maxBackoff             = 5 * time.Second
)

var (
	ErrInvalidConfig       = errors.New("vertexai: invalid configuration")
	ErrInvalidRequest      = errors.New("vertexai: invalid request")
	ErrQuotaExhausted      = errors.New("vertexai: quota exhausted")
	ErrUpstreamUnavailable = errors.New("vertexai: upstream unavailable")
	ErrDecodeFailed        = errors.New("vertexai: response decode failed")
)

// Config contains the environment-owned Vertex AI settings. MaxRetries is the
// number of technical retries after the initial request.
type Config struct {
	ProjectID      string
	Location       string
	Model          string
	EmbeddingModel string
	MaxRetries     int
}

// ConfigFromEnv loads and validates the Vertex AI configuration. A missing or
// non-integer AI_TECHNICAL_MAX_RETRIES uses DefaultTechnicalMaxRetries; valid
// integers are clamped to the supported range of zero through ten.
func ConfigFromEnv() (Config, error) {
	config := Config{
		ProjectID:      strings.TrimSpace(os.Getenv("GCP_PROJECT_ID")),
		Location:       strings.TrimSpace(os.Getenv("VERTEX_AI_LOCATION")),
		Model:          strings.TrimSpace(os.Getenv("VERTEX_AI_MODEL")),
		EmbeddingModel: strings.TrimSpace(os.Getenv("VERTEX_EMBEDDING_MODEL")),
		MaxRetries:     DefaultTechnicalMaxRetries,
	}
	if raw := strings.TrimSpace(os.Getenv("AI_TECHNICAL_MAX_RETRIES")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			config.MaxRetries = clampRetries(parsed)
		}
	}
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

// FileInput identifies a backend-validated object that Vertex AI may read.
type FileInput struct {
	URI      string
	MIMEType string
}

// Request is shared by analysis and verifier callers. The client does not add
// prompts, persist results, or mutate workflow state.
type Request struct {
	TextParts []string
	FileParts []FileInput
}

// Verdict is populated when the generated text is a JSON object with a
// supported verifier status. FAIL is a successful semantic response.
type Verdict string

const (
	VerdictUnknown         Verdict = ""
	VerdictPass            Verdict = "PASS"
	VerdictPassWithWarning Verdict = "PASS_WITH_WARNING"
	VerdictFail            Verdict = "FAIL"
)

// Response contains generated text and call telemetry. Attempts includes the
// initial attempt. Duration covers retries and backoff.
type Response struct {
	Text     string
	Verdict  Verdict
	Attempts int
	Duration time.Duration
}

// Client calls the Vertex AI generateContent REST endpoint.
type Client struct {
	config      Config
	accessToken string
	httpClient  *http.Client
	logger      *slog.Logger
	endpoint    string
	backoffBase time.Duration
}

// New constructs a Vertex AI client. Production wiring must obtain the bearer
// token through ADC/service-account identity and manage token refresh; this
// package deliberately contains no fake credential or authentication bypass.
func New(config Config, accessToken string, httpClient *http.Client, logger *slog.Logger) (*Client, error) {
	config.ProjectID = strings.TrimSpace(config.ProjectID)
	config.Location = strings.TrimSpace(config.Location)
	config.Model = strings.TrimSpace(config.Model)
	config.EmbeddingModel = strings.TrimSpace(config.EmbeddingModel)
	config.MaxRetries = clampRetries(config.MaxRetries)
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if strings.TrimSpace(accessToken) == "" {
		return nil, fmt.Errorf("%w: Vertex AI access token is required", ErrInvalidConfig)
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if logger == nil {
		logger = slog.Default()
	}

	endpoint := fmt.Sprintf(
		"https://%s-aiplatform.googleapis.com/v1/projects/%s/locations/%s/publishers/google/models/%s:generateContent",
		config.Location,
		url.PathEscape(config.ProjectID),
		url.PathEscape(config.Location),
		url.PathEscape(config.Model),
	)
	return &Client{
		config:      config,
		accessToken: strings.TrimSpace(accessToken),
		httpClient:  httpClient,
		logger:      logger,
		endpoint:    endpoint,
		backoffBase: defaultBackoff,
	}, nil
}

// NewFromEnv reads Config from the environment and constructs a Client.
func NewFromEnv(accessToken string, httpClient *http.Client, logger *slog.Logger) (*Client, error) {
	config, err := ConfigFromEnv()
	if err != nil {
		return nil, err
	}
	return New(config, accessToken, httpClient, logger)
}

// Config returns a copy of the client's effective configuration.
func (c *Client) Config() Config {
	return c.config
}

type generateRequest struct {
	Contents []content `json:"contents"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text     string    `json:"text,omitempty"`
	FileData *fileData `json:"fileData,omitempty"`
}

type fileData struct {
	FileURI  string `json:"fileUri"`
	MIMEType string `json:"mimeType"`
}

type generateResponse struct {
	Candidates []struct {
		Content content `json:"content"`
	} `json:"candidates"`
}

// GenerateContent submits one analysis or verifier request. Only transport
// failures, HTTP 429, and HTTP 5xx responses consume the technical retry
// budget. Model output, including a FAIL verdict, never triggers a retry.
func (c *Client) GenerateContent(ctx context.Context, request Request) (Response, error) {
	started := time.Now()
	parts, mimeTypes, err := validateRequest(request)
	if err != nil {
		c.logAttempt(ctx, 0, 0, 0, "permanent-fail", nil)
		return responseTelemetry(started, 0), err
	}
	payload, err := json.Marshal(generateRequest{Contents: []content{{Role: "user", Parts: parts}}})
	if err != nil {
		return responseTelemetry(started, 0), fmt.Errorf("%w: encode request: %v", ErrInvalidRequest, err)
	}

	maxAttempts := c.config.MaxRetries + 1
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attemptStarted := time.Now()
		status, body, callErr := c.call(ctx, payload)
		latency := time.Since(attemptStarted)

		if callErr != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				c.logAttempt(ctx, attempt, status, latency, "canceled", mimeTypes)
				return responseTelemetry(started, attempt), ctxErr
			}
			if errors.Is(callErr, ErrDecodeFailed) || errors.Is(callErr, ErrInvalidRequest) {
				c.logAttempt(ctx, attempt, status, latency, "permanent-fail", mimeTypes)
				return responseTelemetry(started, attempt), callErr
			}
			if attempt < maxAttempts {
				c.logAttempt(ctx, attempt, status, latency, "retryable-fail", mimeTypes)
				if err := c.waitForRetry(ctx, attempt); err != nil {
					return responseTelemetry(started, attempt), err
				}
				continue
			}
			c.logAttempt(ctx, attempt, status, latency, "retryable-fail", mimeTypes)
			return responseTelemetry(started, attempt), fmt.Errorf("call Vertex AI generateContent: %w", ErrUpstreamUnavailable)
		}

		if status == http.StatusTooManyRequests || status >= http.StatusInternalServerError {
			if attempt < maxAttempts {
				c.logAttempt(ctx, attempt, status, latency, "retryable-fail", mimeTypes)
				if err := c.waitForRetry(ctx, attempt); err != nil {
					return responseTelemetry(started, attempt), err
				}
				continue
			}
			c.logAttempt(ctx, attempt, status, latency, "retryable-fail", mimeTypes)
			if status == http.StatusTooManyRequests {
				return responseTelemetry(started, attempt), fmt.Errorf("Vertex AI generateContent returned HTTP %d: %w", status, ErrQuotaExhausted)
			}
			return responseTelemetry(started, attempt), fmt.Errorf("Vertex AI generateContent returned HTTP %d: %w", status, ErrUpstreamUnavailable)
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			c.logAttempt(ctx, attempt, status, latency, "permanent-fail", mimeTypes)
			return responseTelemetry(started, attempt), fmt.Errorf("Vertex AI generateContent returned HTTP %d: %w", status, ErrInvalidRequest)
		}

		text, err := decodeGeneratedText(body)
		if err != nil {
			c.logAttempt(ctx, attempt, status, latency, "permanent-fail", mimeTypes)
			return responseTelemetry(started, attempt), err
		}
		c.logAttempt(ctx, attempt, status, latency, "success", mimeTypes)
		result := responseTelemetry(started, attempt)
		result.Text = text
		result.Verdict = ParseVerdict(text)
		return result, nil
	}

	panic("unreachable")
}

func (c *Client) call(ctx context.Context, payload []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, nil, fmt.Errorf("%w: create HTTP request", ErrInvalidRequest)
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(req)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return 0, nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		// Provider error bodies are deliberately not surfaced: they may echo
		// request material. The HTTP status is sufficient for translation.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxVertexResponseBytes))
		return response.StatusCode, nil, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxVertexResponseBytes+1))
	if err != nil {
		return response.StatusCode, nil, fmt.Errorf("%w: read response", ErrDecodeFailed)
	}
	if len(body) > maxVertexResponseBytes {
		return response.StatusCode, nil, fmt.Errorf("%w: response exceeds %d bytes", ErrDecodeFailed, maxVertexResponseBytes)
	}
	return response.StatusCode, body, nil
}

func validateConfig(config Config) error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "GCP_PROJECT_ID", value: config.ProjectID},
		{name: "VERTEX_AI_LOCATION", value: config.Location},
		{name: "VERTEX_AI_MODEL", value: config.Model},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("%w: %s is required", ErrInvalidConfig, field.name)
		}
	}
	if strings.ContainsAny(config.Location, "/?#") || strings.ContainsAny(config.Location, " \t\r\n") {
		return fmt.Errorf("%w: VERTEX_AI_LOCATION is invalid", ErrInvalidConfig)
	}
	return nil
}

func validateRequest(request Request) ([]part, []string, error) {
	if len(request.TextParts) == 0 && len(request.FileParts) == 0 {
		return nil, nil, fmt.Errorf("%w: at least one text or file part is required", ErrInvalidRequest)
	}
	parts := make([]part, 0, len(request.TextParts)+len(request.FileParts))
	for i, text := range request.TextParts {
		if strings.TrimSpace(text) == "" {
			return nil, nil, fmt.Errorf("%w: text part %d is empty", ErrInvalidRequest, i)
		}
		parts = append(parts, part{Text: text})
	}
	mimeTypes := make([]string, 0, len(request.FileParts))
	for i, file := range request.FileParts {
		if !supportedMIMEType(file.MIMEType) {
			return nil, nil, fmt.Errorf("%w: file part %d has unsupported MIME type", ErrInvalidRequest, i)
		}
		if !validGCSURI(file.URI) {
			return nil, nil, fmt.Errorf("%w: file part %d must use a valid gs:// URI", ErrInvalidRequest, i)
		}
		parts = append(parts, part{FileData: &fileData{FileURI: file.URI, MIMEType: file.MIMEType}})
		mimeTypes = append(mimeTypes, file.MIMEType)
	}
	return parts, mimeTypes, nil
}

func supportedMIMEType(mimeType string) bool {
	switch mimeType {
	case "application/pdf", "image/jpeg", "image/png":
		return true
	default:
		return false
	}
}

func validGCSURI(raw string) bool {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "gs" || parsed.Opaque != "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return parsed.Host != "" && parsed.Host == parsed.Hostname() && strings.Trim(parsed.EscapedPath(), "/") != ""
}

func decodeGeneratedText(body []byte) (string, error) {
	var decoded generateResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", fmt.Errorf("%w: invalid JSON", ErrDecodeFailed)
	}
	if len(decoded.Candidates) == 0 {
		return "", fmt.Errorf("%w: response has no candidates", ErrDecodeFailed)
	}
	var textParts []string
	for _, candidate := range decoded.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				textParts = append(textParts, part.Text)
			}
		}
	}
	if len(textParts) == 0 {
		return "", fmt.Errorf("%w: response has no generated text", ErrDecodeFailed)
	}
	return strings.Join(textParts, "\n"), nil
}

// ParseVerdict extracts a verifier status from a JSON object. Markdown JSON
// fences are accepted because Gemini may retain them despite prompt guidance.
func ParseVerdict(text string) Verdict {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "```") {
		if newline := strings.IndexByte(trimmed, '\n'); newline >= 0 {
			trimmed = strings.TrimSpace(trimmed[newline+1:])
			trimmed = strings.TrimSuffix(trimmed, "```")
			trimmed = strings.TrimSpace(trimmed)
		}
	}
	var output struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(trimmed), &output); err != nil {
		return VerdictUnknown
	}
	switch Verdict(strings.ToUpper(strings.TrimSpace(output.Status))) {
	case VerdictPass:
		return VerdictPass
	case VerdictPassWithWarning:
		return VerdictPassWithWarning
	case VerdictFail:
		return VerdictFail
	default:
		return VerdictUnknown
	}
}

func (c *Client) waitForRetry(ctx context.Context, failedAttempt int) error {
	if c.backoffBase <= 0 {
		return nil
	}
	delay := c.backoffBase
	for i := 1; i < failedAttempt && delay < maxBackoff; i++ {
		delay *= 2
		if delay > maxBackoff {
			delay = maxBackoff
		}
	}
	jittered := time.Duration(float64(delay) * (0.5 + rand.Float64()))
	timer := time.NewTimer(jittered)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) logAttempt(ctx context.Context, attempt, status int, latency time.Duration, outcome string, mimeTypes []string) {
	logging.WithLogger(ctx, c.logger).Info("Vertex AI generateContent attempt",
		"component", "vertexai",
		"model", c.config.Model,
		"attempt", attempt,
		"max_attempts", c.config.MaxRetries+1,
		"latency_ms", float64(latency.Microseconds())/1000,
		"http_status", status,
		"outcome", outcome,
		"file_mime_types", mimeTypes,
	)
}

func responseTelemetry(started time.Time, attempts int) Response {
	return Response{Attempts: attempts, Duration: time.Since(started)}
}

func clampRetries(retries int) int {
	if retries < 0 {
		return 0
	}
	if retries > MaxTechnicalMaxRetries {
		return MaxTechnicalMaxRetries
	}
	return retries
}
