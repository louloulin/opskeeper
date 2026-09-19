package demo

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	demomodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/demo"
)

func TestMatrixWorkflowPublisherSignsAndSendsAuthorityEvent(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer matrix-token" {
			t.Errorf("authorization = %q", request.Header.Get("Authorization"))
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = writer.Write([]byte(`{"event_id":"$authority"}`))
	}))
	defer server.Close()

	publisher, err := NewMatrixWorkflowPublisher(
		server.URL, "matrix-token", "!room:hs", "@manager:hs", "0123456789abcdef",
	)
	if err != nil {
		t.Fatal(err)
	}
	run := &demomodel.ScenarioRun{IncidentID: 100, TargetFingerprint: "0123456789abcdef"}
	decision := &PreviewDecisionSummary{ReplayProfileID: "sha256:workload-v1", CandidateA: "candidate-a"}
	if err := publisher.PublishWorkflow(context.Background(), run, "awaiting_approval", decision); err != nil {
		t.Fatal(err)
	}
	authority := body["opskeeper.authority"].(map[string]any)
	token := authority["token"].(string)
	encodedClaims, signature, _ := strings.Cut(token, ".")
	claims, err := base64.RawURLEncoding.DecodeString(encodedClaims)
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte("0123456789abcdef"))
	_, _ = mac.Write(claims)
	if !hmac.Equal([]byte(signature), []byte(hex.EncodeToString(mac.Sum(nil)))) {
		t.Fatal("invalid authority signature")
	}
	var authorityClaims struct {
		ManagerID string `json:"manager_id"`
		RoomID    string `json:"room_id"`
		Stage     string `json:"stage"`
		Nonce     string `json:"nonce"`
	}
	if err := json.Unmarshal(claims, &authorityClaims); err != nil {
		t.Fatal(err)
	}
	if authorityClaims.ManagerID != "@manager:hs" || authorityClaims.RoomID != "!room:hs" ||
		authorityClaims.Stage != "awaiting_approval" || len(authorityClaims.Nonce) < 32 {
		t.Fatalf("authority claims = %+v", authorityClaims)
	}
	workflow := body["agentteams.workflow"].(map[string]any)
	if workflow["runId"] != "100" || workflow["authorityStage"] != "awaiting_approval" {
		t.Fatalf("workflow = %+v", workflow)
	}
	if workflow["title"] != "OpsKeeper 事故恢复 100" || workflow["status"] != "in_progress" {
		t.Fatalf("workflow display fields = %+v", workflow)
	}
	rawSteps := workflow["steps"].([]any)
	if len(rawSteps) != 4 {
		t.Fatalf("workflow steps = %+v", rawSteps)
	}
	steps := make([]map[string]any, 0, len(rawSteps))
	for _, rawStep := range rawSteps {
		steps = append(steps, rawStep.(map[string]any))
	}
	if steps[0]["status"] != "completed" || steps[1]["status"] != "in_progress" {
		t.Fatalf("workflow steps = %+v", steps)
	}
	utcTime := authority["time_utc"].(string)
	beijingTime := authority["time_bjt"].(string)
	if !strings.HasSuffix(utcTime, "Z") || !strings.HasSuffix(beijingTime, "+08:00") {
		t.Fatalf("authority timestamps must be labelled UTC and UTC+8: utc=%q bjt=%q", utcTime, beijingTime)
	}
	if message := body["body"].(string); !strings.Contains(message, "time_utc=") || !strings.Contains(message, "time_bjt=") {
		t.Fatalf("authority message must expose both timezones: %q", message)
	}
}

func TestMatrixWorkflowPublisherRequiresCompleteConfiguration(t *testing.T) {
	if _, err := NewMatrixWorkflowPublisher("", "token", "!room", "@manager:hs", "0123456789abcdef"); err == nil {
		t.Fatal("expected missing URL failure")
	}
	if _, err := NewMatrixWorkflowPublisher("http://matrix", "token", "#room", "@manager:hs", "0123456789abcdef"); err == nil {
		t.Fatal("expected room failure")
	}
	if _, err := NewMatrixWorkflowPublisher("http://matrix", "token", "!room", "@manager:hs", "short"); err == nil {
		t.Fatal("expected secret failure")
	}
}
