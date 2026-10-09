package seed

import (
	"reflect"
	"strings"
	"testing"
)

func TestFixturesAreDeterministicAndValid(t *testing.T) {
	first := fixtureData()
	second := fixtureData()
	if !reflect.DeepEqual(first, second) {
		t.Fatal("fixtureData() returned different values")
	}
	if err := validateFixtures(first); err != nil {
		t.Fatalf("validateFixtures() error = %v", err)
	}
}

func TestFixturePersonasAndRoleSeparation(t *testing.T) {
	data := fixtureData()
	wantRoles := map[string]string{
		"Operations User":  "USER",
		"Risk User":        "USER",
		"Development User": "USER",
		"Manager User":     "ADMIN",
	}
	for _, user := range data.users {
		if wantRoles[user.name] != user.systemRole {
			t.Errorf("%s system role = %q, want %q", user.name, user.systemRole, wantRoles[user.name])
		}
		if !strings.HasSuffix(user.email, "@jawir-sentinel.example") {
			t.Errorf("%s email %q is not in the reserved example domain", user.name, user.email)
		}
	}

	wantRoleUsers := map[string]string{
		"MAKER":    operationsUserID,
		"CHECKER":  riskUserID,
		"SIGNER":   managerUserID,
		"EXECUTER": developmentID,
	}
	statuses := make(map[string]bool)
	for _, demoCase := range data.cases {
		statuses[demoCase.status] = true
		if len(demoCase.participants) != 4 {
			t.Fatalf("case %s has %d participants, want 4", demoCase.caseNumber, len(demoCase.participants))
		}
		seenUsers := make(map[string]bool)
		seenRoles := make(map[string]bool)
		for _, participant := range demoCase.participants {
			if seenUsers[participant.userID] {
				t.Errorf("case %s assigns user %s more than once", demoCase.caseNumber, participant.userID)
			}
			if seenRoles[participant.role] {
				t.Errorf("case %s assigns role %s more than once", demoCase.caseNumber, participant.role)
			}
			if !participant.required {
				t.Errorf("case %s role %s is not required", demoCase.caseNumber, participant.role)
			}
			if wantRoleUsers[participant.role] != participant.userID {
				t.Errorf("case %s role %s user = %s, want %s", demoCase.caseNumber, participant.role, participant.userID, wantRoleUsers[participant.role])
			}
			seenUsers[participant.userID] = true
			seenRoles[participant.role] = true
		}
	}
	if !statuses["DRAFT"] || !statuses["CHECKING"] {
		t.Errorf("case statuses = %v, want DRAFT and CHECKING", statuses)
	}
}

func TestParseUUIDRejectsInvalidValue(t *testing.T) {
	if _, err := parseUUID("not-a-uuid"); err == nil {
		t.Fatal("parseUUID() error = nil")
	}
}
