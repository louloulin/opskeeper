package server

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/audit"
	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/cmdpolicy"
	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/host_files"
	"github.com/vincent-wuhan/opskeeper/internal/edgeagent/restart_service"
)

// helper: fresh audit chain with a random key.
func newAudit(t *testing.T, edgeID string) *audit.Chain {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatal(err)
	}
	c, err := audit.NewChain(k, edgeID, nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// allowAllValidator is the permissive path validator used by tests
// that don't care about host_files path gating. Production code
// wires host_files.SandboxConfig which implements cmdpolicy's
// PathValidator interface.
type allowAllValidator struct{}

func (allowAllValidator) ValidatePath(string) error { return nil }

func newServerClean(t *testing.T) *Server {
	t.Helper()
	s := New()
	s.EdgeID = "edge-test"
	s.Bash = &cmdpolicy.Sandbox{
		Policy:        cmdpolicy.DefaultReadOnly(),
		PathValidator: allowAllValidator{},
	}
	s.Audit = newAudit(t, s.EdgeID)
	s.HostFiles = host_files.DefaultSandboxConfig()
	s.Restart = &restart_service.SandboxConfig{
		AllowedUnits: []string{"nginx", "sshd"},
		Mocked:       true,
	}
	return s
}

func TestHealth_OK(t *testing.T) {
	s := newServerClean(t)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["status"] != "ok" {
		t.Errorf("status=%q", resp["status"])
	}
	if resp["edge_id"] != "edge-test" {
		t.Errorf("edge_id=%q", resp["edge_id"])
	}
}

func TestHealth_AuditUnconfigured(t *testing.T) {
	s := New()
	s.EdgeID = "edge-x"
	s.Bash = nil
	s.Audit = nil
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status=%d, want 503", w.Code)
	}
}

func TestBash_ReadModeAllowed(t *testing.T) {
	s := newServerClean(t)
	body := bytes.NewReader([]byte(`{"cmd":"ps aux","mode":"read"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/edge/tools/bash", body))
	// ps is in DefaultReadOnly; allowAllValidator makes paths OK;
	// exec should succeed.
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp bashResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Allowed {
		t.Errorf("Allowed=false; Reason=%s", resp.Reason)
	}
	if resp.AuditSeq == 0 {
		t.Errorf("AuditSeq=0; expected chain.Append to have run")
	}
}

func TestBash_ReadModeDeniedByPolicy(t *testing.T) {
	s := newServerClean(t)
	body := bytes.NewReader([]byte(`{"cmd":"rm -rf /tmp/x","mode":"read"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/edge/tools/bash", body))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestBash_WriteModeRequiresToken(t *testing.T) {
	s := newServerClean(t)
	body := bytes.NewReader([]byte(`{"cmd":"systemctl restart nginx","mode":"write","proposal_id":"p-1"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/edge/tools/bash", body))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "approval") {
		t.Errorf("expected approval error, got %s", w.Body.String())
	}
}

func TestBash_WriteModeWithoutApprovalsCacheFailsClosed(t *testing.T) {
	s := newServerClean(t)
	s.Approvals = nil
	body := bytes.NewReader([]byte(`{"cmd":"systemctl restart nginx","mode":"write","proposal_id":"p-1"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/edge/tools/bash", body))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status=%d, want 401 (fail-closed)", w.Code)
	}
}

func TestBash_WriteModeConsumesToken(t *testing.T) {
	s := newServerClean(t)
	// Wire an approval cache; pre-populate with a matching token.
	appr := cmdpolicy.NewApprovalCache()
	now := time.Now()
	appr.Put(&cmdpolicy.ApprovalToken{
		TokenID: "tk-1", ProposalID: "p-1", Kind: "bash_write", EdgeID: s.EdgeID,
		GrantedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	s.Approvals = appr

	body := bytes.NewReader([]byte(`{"cmd":"systemctl restart nginx","mode":"write","proposal_id":"p-1"}`))
	r := httptest.NewRequest(http.MethodPost, "/v1/edge/tools/bash", body)
	r.Header.Set("X-Opskeeper-Pi-Approval-Token", "tk-1")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	// systemctl is in DefaultReadOnly with read=allowed; write semantics
	// are rejected by the policy because the matcher doesn't match a
	// known read form — wait, systemctl restart hits WriteMatchers
	// and is rejected with 403 by the policy. That's the correct
	// behaviour: even with a valid token, the policy says no.
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// Verify the token was consumed (single-use semantics).
	if _, err := appr.Consume("tk-1", "p-1", "bash_write", s.EdgeID); err == nil {
		t.Errorf("token should have been consumed by the prior request")
	}
}

func TestBash_InvalidJSON(t *testing.T) {
	s := newServerClean(t)
	body := bytes.NewReader([]byte(`{not-json`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/edge/tools/bash", body))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d", w.Code)
	}
}

func TestBash_EmptyCmd(t *testing.T) {
	s := newServerClean(t)
	body := bytes.NewReader([]byte(`{"cmd":""}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/edge/tools/bash", body))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d", w.Code)
	}
}

func TestRestart_AllowedUnitWithMock(t *testing.T) {
	s := newServerClean(t)
	appr := cmdpolicy.NewApprovalCache()
	now := time.Now()
	appr.Put(&cmdpolicy.ApprovalToken{
		TokenID: "tk-r", ProposalID: "p-r", Kind: "restart_service", EdgeID: s.EdgeID,
		GrantedAt: now, ExpiresAt: now.Add(time.Minute),
	})
	s.Approvals = appr

	body := bytes.NewReader([]byte(`{"service":"nginx","proposal_id":"p-r"}`))
	r := httptest.NewRequest(http.MethodPost, "/v1/edge/tools/host_restart_service", body)
	r.Header.Set("X-Opskeeper-Pi-Approval-Token", "tk-r")
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp restartResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Allowed || !resp.Mocked {
		t.Errorf("resp=%+v", resp)
	}
	if resp.AuditSeq == 0 {
		t.Errorf("expected audit sequence; got 0")
	}
}

func TestRestart_RejectsUnknownUnit(t *testing.T) {
	s := newServerClean(t)
	body := bytes.NewReader([]byte(`{"service":"bogus","proposal_id":"p-1"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/edge/tools/host_restart_service", body))
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestRestart_RequiresToken(t *testing.T) {
	s := newServerClean(t)
	body := bytes.NewReader([]byte(`{"service":"nginx","proposal_id":"p-1"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/edge/tools/host_restart_service", body))
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status=%d", w.Code)
	}
}

func TestPropose_RecordsAudit(t *testing.T) {
	s := newServerClean(t)
	body := bytes.NewReader([]byte(`{"kind":"restart_service","proposal_id":"p-1","rationale":"nginx OOM"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/edge/propose", body))
	if w.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if s.Audit.Len() != 1 {
		t.Errorf("audit.Len=%d, want 1", s.Audit.Len())
	}
	last, _ := s.Audit.Last()
	if last.Kind != "propose" {
		t.Errorf("audit Kind=%q", last.Kind)
	}
}

func TestPropose_RequiresProposalID(t *testing.T) {
	s := newServerClean(t)
	body := bytes.NewReader([]byte(`{"kind":"x","rationale":"y"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/edge/propose", body))
	if w.Code != http.StatusBadRequest {
		t.Errorf("status=%d", w.Code)
	}
}

func TestAudit_FilterByKind(t *testing.T) {
	s := newServerClean(t)
	for i := 0; i < 5; i++ {
		s.Audit.Append("tool.call.bash", []byte(`{"x":1}`), "")
	}
	for i := 0; i < 3; i++ {
		s.Audit.Append("propose", []byte(`{"x":2}`), "")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/edge/audit?kind=propose", nil)
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp auditListResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Count != 3 {
		t.Errorf("Count=%d, want 3", resp.Count)
	}
	for _, e := range resp.Events {
		if e.Kind != "propose" {
			t.Errorf("got non-propose event: %+v", e)
		}
	}
}

func TestAudit_FilterBySince(t *testing.T) {
	s := newServerClean(t)
	for i := 0; i < 5; i++ {
		s.Audit.Append("tool.call.bash", []byte(`{"x":1}`), "")
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/v1/edge/audit?since=2", nil)
	s.Handler().ServeHTTP(w, r)
	var resp auditListResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Count != 3 {
		t.Errorf("Count=%d, want 3 (events 3,4,5)", resp.Count)
	}
	if resp.NextPage != 5 {
		t.Errorf("NextPage=%d, want 5", resp.NextPage)
	}
}

func TestHostFilesCheck_PathAllowed(t *testing.T) {
	s := newServerClean(t)
	body := bytes.NewReader([]byte(`{"path":"/var/log/syslog"}`))
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/edge/tools/host_files/check", body))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	var resp hostFilesCheckResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Allowed {
		t.Errorf("Allowed=false; Reason=%s", resp.Reason)
	}
}

func TestEnforceLoopback(t *testing.T) {
	s := New()
	if err := s.enforceLoopback("127.0.0.1:9101"); err != nil {
		t.Errorf("loopback v4 should pass: %v", err)
	}
	if err := s.enforceLoopback("[::1]:9101"); err != nil {
		t.Errorf("loopback v6 should pass: %v", err)
	}
	if err := s.enforceLoopback("0.0.0.0:9101"); err == nil {
		t.Errorf("any-address should be rejected")
	}
	if err := s.enforceLoopback("192.0.2.1:9101"); err == nil {
		t.Errorf("non-loopback v4 should be rejected")
	}
	if err := s.enforceLoopback("[2001:db8::1]:9101"); err == nil {
		t.Errorf("non-loopback v6 should be rejected")
	}
	if err := s.enforceLoopback("not-an-addr"); err == nil {
		t.Errorf("malformed should be rejected")
	}
	if err := s.enforceLoopback(":9101"); err == nil {
		t.Errorf("empty host should be rejected")
	}
}