package demo

import "testing"

func TestRepairPreviewExecutorFailsClosedWithoutConfiguration(t *testing.T) {
	if _, err := NewRepairPreviewExecutor("", "", "sha256:workload", nil); err == nil {
		t.Fatal("expected empty DSN to fail")
	}
	if _, err := NewRepairPreviewExecutor("postgres://preview", "", "", nil); err == nil {
		t.Fatal("expected empty workload fingerprint to fail")
	}
}

func TestRepairPreviewExecutorRejectsUnexpectedWorkloadProfile(t *testing.T) {
	_, err := NewRepairPreviewExecutor(
		"postgres://preview", "../../../deploy/repair-preview/pg-pool-workload.yaml", "sha256:not-current", nil,
	)
	if err == nil {
		t.Fatal("expected workload fingerprint mismatch to fail")
	}
}

func TestDeterministicPreviewRunIDIsStable(t *testing.T) {
	left := DeterministicPreviewRunID(1, ScenarioID, "final-demo-key", 100)
	right := DeterministicPreviewRunID(1, ScenarioID, "final-demo-key", 100)
	if left == "" || left != right {
		t.Fatalf("run IDs = %q/%q", left, right)
	}
	if left == DeterministicPreviewRunID(1, ScenarioID, "final-demo-key-2", 100) {
		t.Fatal("expected a distinct run ID for a distinct idempotency key")
	}
}
