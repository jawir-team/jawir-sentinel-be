// Package seed installs deterministic, fictional demonstration data.
package seed

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/jawir-team/jawir-sentinel-be/internal/db/sqlc"
)

const (
	operationsUserID = "20000000-0000-4000-8000-000000000001"
	riskUserID       = "20000000-0000-4000-8000-000000000002"
	developmentID    = "20000000-0000-4000-8000-000000000003"
	managerUserID    = "20000000-0000-4000-8000-000000000004"
)

// Queries is the database boundary needed by Run. *db.Queries implements it.
type Queries interface {
	SeedUnit(context.Context, db.SeedUnitParams) (int64, error)
	SeedUser(context.Context, db.SeedUserParams) (int64, error)
	SeedCaseType(context.Context, db.SeedCaseTypeParams) (int64, error)
	SeedPolicy(context.Context, db.SeedPolicyParams) (int64, error)
	SeedPolicyVersion(context.Context, db.SeedPolicyVersionParams) (int64, error)
	SeedCase(context.Context, db.SeedCaseParams) (int64, error)
	SeedCaseParticipant(context.Context, db.SeedCaseParticipantParams) (int64, error)
	SeedEvidence(context.Context, db.SeedEvidenceParams) (int64, error)
	SeedCompletedAnalysis(context.Context, db.SeedCompletedAnalysisParams) (int64, error)
	SeedCaseCurrentAnalysis(context.Context, db.SeedCaseCurrentAnalysisParams) (int64, error)
}

var _ Queries = (*db.Queries)(nil)

// Summary reports rows inserted (or linked for CurrentAnalyses). Existing rows
// skipped by ON CONFLICT DO NOTHING are not included.
type Summary struct {
	Units           int64
	Users           int64
	CaseTypes       int64
	Policies        int64
	PolicyVersions  int64
	Cases           int64
	Participants    int64
	Evidences       int64
	Analyses        int64
	CurrentAnalyses int64
}

type unitFixture struct {
	id, code, name, description string
}

type userFixture struct {
	id, unitID, firebaseUID, name, email, systemRole string
}

type caseTypeFixture struct {
	id, code, name, description string
}

type policyFixture struct {
	id, caseTypeID, versionID                 string
	code, title, domain, description, content string
}

type caseFixture struct {
	id, caseNumber, caseTypeID, title, description string
	urgency, status                                string
	participants                                   []participantFixture
	evidences                                      []evidenceFixture
	analysis                                       *analysisFixture
}

type participantFixture struct {
	id, userID, role string
	required         bool
}

type evidenceFixture struct {
	id, userID, sourceType, title, content string
}

type analysisFixture struct {
	id, summary string
}

type fixtures struct {
	units     []unitFixture
	users     []userFixture
	caseTypes []caseTypeFixture
	policies  []policyFixture
	cases     []caseFixture
}

func fixtureData() fixtures {
	return fixtures{
		units: []unitFixture{
			{"10000000-0000-4000-8000-000000000001", "DEMO_OPERATIONS", "Fictional Operations Lab", "Synthetic demonstration unit; not a real organization."},
			{"10000000-0000-4000-8000-000000000002", "DEMO_RISK", "Fictional Risk Lab", "Synthetic demonstration unit; not a real risk function."},
			{"10000000-0000-4000-8000-000000000003", "DEMO_DEVELOPMENT", "Fictional Development Lab", "Synthetic demonstration unit; not a real engineering team."},
			{"10000000-0000-4000-8000-000000000004", "DEMO_MANAGEMENT", "Fictional Management Lab", "Synthetic demonstration unit for the admin persona."},
		},
		users: []userFixture{
			{operationsUserID, "10000000-0000-4000-8000-000000000001", "demo-operations-user", "Operations User", "ops.demo@jawir-sentinel.example", "USER"},
			{riskUserID, "10000000-0000-4000-8000-000000000002", "demo-risk-user", "Risk User", "risk.demo@jawir-sentinel.example", "USER"},
			{developmentID, "10000000-0000-4000-8000-000000000003", "demo-development-user", "Development User", "development.demo@jawir-sentinel.example", "USER"},
			{managerUserID, "10000000-0000-4000-8000-000000000004", "demo-manager-user", "Manager User", "manager.demo@jawir-sentinel.example", "ADMIN"},
		},
		caseTypes: []caseTypeFixture{
			{"30000000-0000-4000-8000-000000000001", "CREDIT_REVIEW", "Synthetic Credit Review", "Fictional demonstration workflow; contains no real credit or customer data."},
			{"30000000-0000-4000-8000-000000000002", "FRAUD_INVESTIGATION", "Synthetic Fraud Investigation", "Fictional demonstration workflow; contains no real fraud or customer data."},
		},
		policies: []policyFixture{
			{
				"40000000-0000-4000-8000-000000000001", "30000000-0000-4000-8000-000000000001", "50000000-0000-4000-8000-000000000001",
				"DEMO_CREDIT_REVIEW", "Synthetic Credit Review Policy", "DEMO_CREDIT",
				"Synthetic demonstration policy only; it is not based on a real bank policy.",
				"SYNTHETIC DEMO CONTENT. This fictional policy asks the maker to document the scenario, the checker to review the supplied fictional facts, the signer to record a decision, and the executer to record the fictional outcome. No real bank, customer, account, or credit data is represented.",
			},
			{
				"40000000-0000-4000-8000-000000000002", "30000000-0000-4000-8000-000000000002", "50000000-0000-4000-8000-000000000002",
				"DEMO_FRAUD_INVESTIGATION", "Synthetic Fraud Investigation Policy", "DEMO_FRAUD",
				"Synthetic demonstration policy only; it is not based on a real bank policy.",
				"SYNTHETIC DEMO CONTENT. This fictional policy requires role-separated review of invented indicators and an explicit fictional execution record. It contains no real bank, customer, transaction, account, or investigation data.",
			},
		},
		cases: []caseFixture{
			{
				id: "60000000-0000-4000-8000-000000000001", caseNumber: "DEMO-2025-0001",
				caseTypeID: "30000000-0000-4000-8000-000000000001", title: "Synthetic credit review draft",
				description: "Fictional draft case for demonstrating maker, checker, signer, and executer assignment. No real customer or financial data.",
				urgency:     "MEDIUM", status: "DRAFT",
				participants: caseParticipants("1"),
				evidences: []evidenceFixture{{
					"80000000-0000-4000-8000-000000000001", operationsUserID, "MAKER", "Synthetic scenario note",
					"Synthetic demo evidence: an invented applicant requested review of a fictional product. No real person, bank, account, or application is represented.",
				}},
			},
			{
				id: "60000000-0000-4000-8000-000000000002", caseNumber: "DEMO-2025-0002",
				caseTypeID: "30000000-0000-4000-8000-000000000002", title: "Synthetic investigation under checking",
				description: "Fictional checking-stage case with a static completed analysis. No real customer, transaction, or investigation data.",
				urgency:     "HIGH", status: "CHECKING",
				participants: caseParticipants("2"),
				evidences: []evidenceFixture{{
					"80000000-0000-4000-8000-000000000002", riskUserID, "CHECKER", "Synthetic indicator review",
					"Synthetic demo evidence: invented indicators were reviewed only to demonstrate the checking workflow. No real records were consulted.",
				}},
				analysis: &analysisFixture{
					"90000000-0000-4000-8000-000000000001",
					"Static synthetic demo analysis: the invented scenario is ready for checker review; this is not a real risk or compliance assessment.",
				},
			},
		},
	}
}

func caseParticipants(caseSuffix string) []participantFixture {
	return []participantFixture{
		{"70000000-0000-4000-8000-0000000000" + caseSuffix + "1", operationsUserID, "MAKER", true},
		{"70000000-0000-4000-8000-0000000000" + caseSuffix + "2", riskUserID, "CHECKER", true},
		{"70000000-0000-4000-8000-0000000000" + caseSuffix + "3", managerUserID, "SIGNER", true},
		{"70000000-0000-4000-8000-0000000000" + caseSuffix + "4", developmentID, "EXECUTER", true},
	}
}

// Run inserts all fixtures in foreign-key order. Every insert is conflict-safe,
// so a second run reports zero for rows that already exist and changes no data.
func Run(ctx context.Context, q Queries) (Summary, error) {
	if q == nil {
		return Summary{}, fmt.Errorf("seed queries are required")
	}
	data := fixtureData()
	if err := validateFixtures(data); err != nil {
		return Summary{}, err
	}

	var summary Summary
	for _, fixture := range data.units {
		rows, err := q.SeedUnit(ctx, db.SeedUnitParams{
			ID: mustUUID(fixture.id), Code: fixture.code, Name: fixture.name, Description: text(fixture.description),
		})
		if err != nil {
			return Summary{}, fmt.Errorf("seed unit %s: %w", fixture.code, err)
		}
		summary.Units += rows
	}
	for _, fixture := range data.users {
		rows, err := q.SeedUser(ctx, db.SeedUserParams{
			ID: mustUUID(fixture.id), UnitID: mustUUID(fixture.unitID), FirebaseUID: fixture.firebaseUID,
			Name: fixture.name, Email: fixture.email, SystemRole: fixture.systemRole,
		})
		if err != nil {
			return Summary{}, fmt.Errorf("seed user %s: %w", fixture.name, err)
		}
		summary.Users += rows
	}
	for _, fixture := range data.caseTypes {
		rows, err := q.SeedCaseType(ctx, db.SeedCaseTypeParams{
			ID: mustUUID(fixture.id), Code: fixture.code, Name: fixture.name, Description: text(fixture.description),
		})
		if err != nil {
			return Summary{}, fmt.Errorf("seed case type %s: %w", fixture.code, err)
		}
		summary.CaseTypes += rows
	}
	for _, fixture := range data.policies {
		rows, err := q.SeedPolicy(ctx, db.SeedPolicyParams{
			ID: mustUUID(fixture.id), Code: fixture.code, Title: fixture.title, Domain: fixture.domain,
			CaseTypeID: mustUUID(fixture.caseTypeID), Description: text(fixture.description),
		})
		if err != nil {
			return Summary{}, fmt.Errorf("seed policy %s: %w", fixture.code, err)
		}
		summary.Policies += rows

		rows, err = q.SeedPolicyVersion(ctx, db.SeedPolicyVersionParams{
			ID: mustUUID(fixture.versionID), PolicyID: mustUUID(fixture.id), Content: fixture.content,
			CreatedBy: mustUUID(managerUserID), ApprovedBy: mustUUID(managerUserID),
		})
		if err != nil {
			return Summary{}, fmt.Errorf("seed policy version %s: %w", fixture.code, err)
		}
		summary.PolicyVersions += rows
	}
	for _, fixture := range data.cases {
		caseID := mustUUID(fixture.id)
		rows, err := q.SeedCase(ctx, db.SeedCaseParams{
			ID: caseID, CaseNumber: fixture.caseNumber, CaseTypeID: mustUUID(fixture.caseTypeID),
			Title: fixture.title, Description: fixture.description, Urgency: fixture.urgency,
			Status: fixture.status, CreatedBy: mustUUID(operationsUserID),
		})
		if err != nil {
			return Summary{}, fmt.Errorf("seed case %s: %w", fixture.caseNumber, err)
		}
		summary.Cases += rows

		for _, participant := range fixture.participants {
			rows, err = q.SeedCaseParticipant(ctx, db.SeedCaseParticipantParams{
				ID: mustUUID(participant.id), CaseID: caseID, UserID: mustUUID(participant.userID),
				Role: participant.role, Required: participant.required, AssignedBy: mustUUID(operationsUserID),
			})
			if err != nil {
				return Summary{}, fmt.Errorf("seed %s participant for case %s: %w", participant.role, fixture.caseNumber, err)
			}
			summary.Participants += rows
		}
		for _, evidence := range fixture.evidences {
			rows, err = q.SeedEvidence(ctx, db.SeedEvidenceParams{
				ID: mustUUID(evidence.id), CaseID: caseID, SourceType: evidence.sourceType,
				SourceUserID: mustUUID(evidence.userID), Title: text(evidence.title), Content: text(evidence.content),
			})
			if err != nil {
				return Summary{}, fmt.Errorf("seed evidence for case %s: %w", fixture.caseNumber, err)
			}
			summary.Evidences += rows
		}
		if fixture.analysis != nil {
			analysisID := mustUUID(fixture.analysis.id)
			rows, err = q.SeedCompletedAnalysis(ctx, db.SeedCompletedAnalysisParams{
				ID: analysisID, CaseID: caseID, Summary: text(fixture.analysis.summary),
			})
			if err != nil {
				return Summary{}, fmt.Errorf("seed analysis for case %s: %w", fixture.caseNumber, err)
			}
			summary.Analyses += rows

			rows, err = q.SeedCaseCurrentAnalysis(ctx, db.SeedCaseCurrentAnalysisParams{
				AnalysisID: analysisID, CaseID: caseID,
			})
			if err != nil {
				return Summary{}, fmt.Errorf("link analysis for case %s: %w", fixture.caseNumber, err)
			}
			summary.CurrentAnalyses += rows
		}
	}
	return summary, nil
}

func validateFixtures(data fixtures) error {
	seen := make(map[pgtype.UUID]string)
	validateID := func(raw, label string) error {
		if _, err := parseUUID(raw); err != nil {
			return fmt.Errorf("invalid fixed UUID for %s: %w", label, err)
		}
		return nil
	}
	checkID := func(raw, label string) error {
		id, err := parseUUID(raw)
		if err != nil {
			return fmt.Errorf("invalid fixed UUID for %s: %w", label, err)
		}
		if prior, exists := seen[id]; exists {
			return fmt.Errorf("duplicate fixed UUID for %s and %s", prior, label)
		}
		seen[id] = label
		return nil
	}
	for _, fixture := range data.units {
		if err := checkID(fixture.id, "unit "+fixture.code); err != nil {
			return err
		}
	}
	for _, fixture := range data.users {
		if err := checkID(fixture.id, "user "+fixture.name); err != nil {
			return err
		}
		if err := validateID(fixture.unitID, "user unit "+fixture.name); err != nil {
			return err
		}
	}
	for _, fixture := range data.caseTypes {
		if err := checkID(fixture.id, "case type "+fixture.code); err != nil {
			return err
		}
	}
	for _, fixture := range data.policies {
		if err := checkID(fixture.id, "policy "+fixture.code); err != nil {
			return err
		}
		if err := validateID(fixture.caseTypeID, "policy case type "+fixture.code); err != nil {
			return err
		}
		if err := checkID(fixture.versionID, "policy version "+fixture.code); err != nil {
			return err
		}
	}
	for _, fixture := range data.cases {
		if err := checkID(fixture.id, "case "+fixture.caseNumber); err != nil {
			return err
		}
		if err := validateID(fixture.caseTypeID, "case type for "+fixture.caseNumber); err != nil {
			return err
		}
		if len(fixture.participants) != 4 {
			return fmt.Errorf("case %s must have exactly four workflow participants", fixture.caseNumber)
		}
		roles := make(map[string]bool)
		users := make(map[string]bool)
		for _, participant := range fixture.participants {
			if err := checkID(participant.id, "participant "+fixture.caseNumber+" "+participant.role); err != nil {
				return err
			}
			if roles[participant.role] || users[participant.userID] || !participant.required {
				return fmt.Errorf("case %s violates required role separation", fixture.caseNumber)
			}
			if err := validateID(participant.userID, "participant user "+fixture.caseNumber+" "+participant.role); err != nil {
				return err
			}
			roles[participant.role] = true
			users[participant.userID] = true
		}
		for _, role := range []string{"MAKER", "CHECKER", "SIGNER", "EXECUTER"} {
			if !roles[role] {
				return fmt.Errorf("case %s is missing required role %s", fixture.caseNumber, role)
			}
		}
		for _, evidence := range fixture.evidences {
			if err := checkID(evidence.id, "evidence "+fixture.caseNumber); err != nil {
				return err
			}
			if err := validateID(evidence.userID, "evidence user "+fixture.caseNumber); err != nil {
				return err
			}
		}
		if fixture.analysis != nil {
			if err := checkID(fixture.analysis.id, "analysis "+fixture.caseNumber); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseUUID(raw string) (pgtype.UUID, error) {
	var id pgtype.UUID
	if err := id.Scan(raw); err != nil {
		return pgtype.UUID{}, err
	}
	if !id.Valid {
		return pgtype.UUID{}, fmt.Errorf("UUID is null")
	}
	return id, nil
}

func mustUUID(raw string) pgtype.UUID {
	id, err := parseUUID(raw)
	if err != nil {
		panic(err)
	}
	return id
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: true}
}
