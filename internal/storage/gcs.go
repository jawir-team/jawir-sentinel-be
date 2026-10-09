package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/option"
)

const (
	defaultSignedURLTTL = 900
	minSignedURLTTL     = 60
	maxSignedURLTTL     = 3600
)

type serviceAccountCredentials struct {
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
}

type gcsStore struct {
	bucket        string
	client        *gcs.Client
	clientErr     error
	credentials   serviceAccountCredentials
	credentialErr error
	ttl           time.Duration
}

var _ Store = (*gcsStore)(nil)

// New configures Google Cloud Storage with the startup-validated bucket. A
// missing bucket disables file storage; other configuration errors are kept
// and returned by the operation that needs the invalid setting.
func New(bucket string) Store {
	bucket = strings.TrimSpace(bucket)
	if bucket == "" {
		return nil
	}

	credentialJSON, credentialErr := readServiceAccountJSON()
	credentials := serviceAccountCredentials{}
	if credentialErr == nil && len(credentialJSON) > 0 {
		if err := json.Unmarshal(credentialJSON, &credentials); err != nil {
			credentialErr = fmt.Errorf("parse GCS service-account credentials: %w", err)
		}
	}

	var (
		client    *gcs.Client
		clientErr error
	)
	if credentialErr != nil {
		clientErr = credentialErr
	} else if len(credentialJSON) > 0 {
		client, clientErr = gcs.NewClient(context.Background(), option.WithCredentialsJSON(credentialJSON))
	} else {
		client, clientErr = gcs.NewClient(context.Background())
	}
	if clientErr != nil {
		clientErr = fmt.Errorf("create GCS client: %w", clientErr)
	}

	return &gcsStore{
		bucket:        bucket,
		client:        client,
		clientErr:     clientErr,
		credentials:   credentials,
		credentialErr: credentialErr,
		ttl:           signedURLTTL(),
	}
}

func (s *gcsStore) SignUploadURL(_ context.Context, key, contentType string) (string, error) {
	if s == nil {
		return "", errors.New("GCS store is not configured")
	}
	if s.credentialErr != nil {
		return "", s.credentialErr
	}
	if strings.TrimSpace(s.credentials.ClientEmail) == "" || strings.TrimSpace(s.credentials.PrivateKey) == "" {
		return "", errors.New("GCS signing requires service-account client_email and private_key credentials")
	}

	url, err := gcs.SignedURL(s.bucket, key, &gcs.SignedURLOptions{
		GoogleAccessID: s.credentials.ClientEmail,
		PrivateKey:     []byte(s.credentials.PrivateKey),
		Method:         http.MethodPut,
		Expires:        time.Now().Add(s.ttl),
		ContentType:    contentType,
		Scheme:         gcs.SigningSchemeV4,
	})
	if err != nil {
		return "", fmt.Errorf("sign GCS upload URL: %w", err)
	}
	return url, nil
}

func (s *gcsStore) StatObject(ctx context.Context, key string) (ObjectAttrs, error) {
	if s == nil {
		return ObjectAttrs{}, errors.New("GCS store is not configured")
	}
	if s.clientErr != nil {
		return ObjectAttrs{}, s.clientErr
	}
	if s.client == nil {
		return ObjectAttrs{}, errors.New("GCS client is not configured")
	}

	attrs, err := s.client.Bucket(s.bucket).Object(key).Attrs(ctx)
	if errors.Is(err, gcs.ErrObjectNotExist) {
		return ObjectAttrs{}, ErrObjectNotFound
	}
	if err != nil {
		return ObjectAttrs{}, fmt.Errorf("stat GCS object: %w", err)
	}
	return ObjectAttrs{ContentType: attrs.ContentType, Size: attrs.Size}, nil
}

func readServiceAccountJSON() ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv("GCS_SERVICE_ACCOUNT_JSON")); raw != "" {
		return []byte(raw), nil
	}
	path := strings.TrimSpace(os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"))
	if path == "" {
		return nil, nil
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read GCS service-account credentials: %w", err)
	}
	return contents, nil
}

func signedURLTTL() time.Duration {
	seconds := defaultSignedURLTTL
	if raw := strings.TrimSpace(os.Getenv("GCS_SIGNED_URL_TTL_SECONDS")); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			seconds = parsed
		}
	}
	if seconds < minSignedURLTTL {
		seconds = minSignedURLTTL
	}
	if seconds > maxSignedURLTTL {
		seconds = maxSignedURLTTL
	}
	return time.Duration(seconds) * time.Second
}
