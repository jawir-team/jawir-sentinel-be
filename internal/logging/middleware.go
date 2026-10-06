package logging

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"strings"
	"sync/atomic"
	"time"
)

const (
	// RequestIDHeader is the HTTP header used to propagate request correlation.
	RequestIDHeader = "X-Request-ID"
	maxRequestIDLen = 128
)

type requestIDContextKey struct{}

var fallbackRequestIDCounter atomic.Uint64

// RequestID propagates a valid caller-supplied request ID or generates a UUID
// v4. The selected ID is available in the request context and response header.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestID := incomingRequestID(r.Header.Values(RequestIDHeader))
		if requestID == "" {
			requestID = newRequestID()
		}

		w.Header().Set(RequestIDHeader, requestID)
		ctx := context.WithValue(r.Context(), requestIDContextKey{}, requestID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequestIDFromContext returns the request ID stored by RequestID, or an empty
// string when ctx is nil or has no request correlation value.
func RequestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	requestID, _ := ctx.Value(requestIDContextKey{}).(string)
	return requestID
}

func incomingRequestID(values []string) string {
	if len(values) != 1 {
		return ""
	}
	value := values[0]
	if value == "" || len(value) > maxRequestIDLen || strings.TrimSpace(value) != value {
		return ""
	}
	for i := 0; i < len(value); i++ {
		// Only reflect bounded, visible ASCII without list separators. This keeps
		// the value safe in both response headers and structured logs.
		if value[i] < 0x21 || value[i] > 0x7e || value[i] == ',' {
			return ""
		}
	}
	return value
}

func newRequestID() string {
	var uuid [16]byte
	if _, err := rand.Read(uuid[:]); err != nil {
		// crypto/rand failures are exceptionally rare. Preserve availability and
		// uniqueness without exposing process or request data in the identifier.
		binary.BigEndian.PutUint64(uuid[:8], uint64(time.Now().UnixNano()))
		binary.BigEndian.PutUint64(uuid[8:], fallbackRequestIDCounter.Add(1))
	}
	uuid[6] = (uuid[6] & 0x0f) | 0x40
	uuid[8] = (uuid[8] & 0x3f) | 0x80

	var encoded [36]byte
	hex.Encode(encoded[0:8], uuid[0:4])
	encoded[8] = '-'
	hex.Encode(encoded[9:13], uuid[4:6])
	encoded[13] = '-'
	hex.Encode(encoded[14:18], uuid[6:8])
	encoded[18] = '-'
	hex.Encode(encoded[19:23], uuid[8:10])
	encoded[23] = '-'
	hex.Encode(encoded[24:36], uuid[10:16])
	return string(encoded[:])
}
