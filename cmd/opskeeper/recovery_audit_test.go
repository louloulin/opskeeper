package main

import (
	"github.com/vincent-wuhan/opskeeper/core/manager/biz/aiops/tools/recovery"
	"reflect"
	"testing"
	"time"

	hitlmodel "github.com/vincent-wuhan/opskeeper/core/manager/model/hitl"
)

func TestRecoveryApprovalQueryFromRequest_PreservesApprovedExecution(t *testing.T) {
	now := time.Date(2026, 8, 26, 0, 48, 8, 0, time.UTC)
	request := recovery.RecoveryProposalRequest{
		ProposalID: "proposal-id",
		SessionID:  "incident-id",
		Kind:       hitlmodel.KindAgentTeams,
		Action:     "kill_process",
		Resource:   "host:fixture",
		Execution: hitlmodel.RecoveryExecutionParameters{
			Command:           "kill_process",
			Reason:            "terminate incident-owned fixture",
			IncidentID:        "incident-id",
			FixtureManifestID: "fixture-manifest-id",
		},
	}

	got := recoveryApprovalQueryFromRequest(request, now)

	wantExecution := request.Execution
	if !reflect.DeepEqual(got.Execution, wantExecution) {
		t.Fatalf("Execution = %+v, want %+v", got.Execution, wantExecution)
	}
	if !got.Now.Equal(now) {
		t.Fatalf("Now = %v, want %v", got.Now, now)
	}
}
