package handler

import (
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
	"github.com/jawir-team/jawir-sentinel-be/internal/httpapi"
)

// requireCurrentAnalysis rejects mutations that do not target the case's
// current analysis. Case submission does not use this guard because it creates
// a fresh analysis ID, and case closure does not accept an analysis ID.
func requireCurrentAnalysis(stored db.Case, analysisID pgtype.UUID) *httpapi.APIError {
	if !stored.CurrentAnalysisID.Valid {
		return participantAPIError(httpapi.CodeInvalidStateTransition, "Case has no current analysis.", nil)
	}
	if stored.CurrentAnalysisID != analysisID {
		return participantAPIError(httpapi.CodeInvalidStateTransition, "Request targets a stale analysis; the case has moved to a newer analysis.", nil)
	}
	return nil
}
