ALTER TABLE case_evidences DROP CONSTRAINT case_evidences_evidence_type_check;
ALTER TABLE case_evidences ADD CONSTRAINT case_evidences_evidence_type_check CHECK (
    evidence_type IN ('COMMENT', 'DOCUMENT', 'LOG', 'SCREENSHOT', 'REFERENCE', 'EXECUTION_RESULT')
);
