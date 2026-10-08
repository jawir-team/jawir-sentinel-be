package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

func TestRequireCurrentAnalysis(t *testing.T) {
	currentID := staleAnalysisTestUUID(1)

	tests := []struct {
		name        string
		stored      db.Case
		analysisID  pgtype.UUID
		wantMessage string
	}{
		{
			name:       "matching analysis",
			stored:     db.Case{CurrentAnalysisID: currentID},
			analysisID: currentID,
		},
		{
			name:        "stale analysis",
			stored:      db.Case{CurrentAnalysisID: currentID},
			analysisID:  staleAnalysisTestUUID(2),
			wantMessage: "Request targets a stale analysis; the case has moved to a newer analysis.",
		},
		{
			name:        "no current analysis",
			stored:      db.Case{},
			analysisID:  currentID,
			wantMessage: "Case has no current analysis.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apiErr := requireCurrentAnalysis(tt.stored, tt.analysisID)
			if tt.wantMessage == "" {
				if apiErr != nil {
					t.Fatalf("requireCurrentAnalysis() error = %v, want nil", apiErr)
				}
				return
			}
			if apiErr == nil {
				t.Fatal("requireCurrentAnalysis() error = nil, want conflict")
			}
			if apiErr.Code != httpapi.CodeInvalidStateTransition {
				t.Errorf("error code = %q, want %q", apiErr.Code, httpapi.CodeInvalidStateTransition)
			}
			if apiErr.Message != tt.wantMessage {
				t.Errorf("error message = %q, want %q", apiErr.Message, tt.wantMessage)
			}

			response := httptest.NewRecorder()
			httpapi.WriteError(response, apiErr)
			if response.Code != http.StatusConflict {
				t.Errorf("HTTP status = %d, want %d", response.Code, http.StatusConflict)
			}
		})
	}
}

func staleAnalysisTestUUID(lastByte byte) pgtype.UUID {
	var value [16]byte
	value[15] = lastByte
	return pgtype.UUID{Bytes: value, Valid: true}
}
