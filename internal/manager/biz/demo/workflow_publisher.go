package demo

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"net/url"

	demomodel "github.com/vincent-wuhan/opskeeper/internal/manager/model/demo"
)

const workflowAuthorityTimeout = 5 * time.Second

type workflowAuthorityClaims struct {
	ManagerID         string    `json:"manager_id"`
	RoomID            string    `json:"room_id"`
	IncidentID        string    `json:"incident_id"`
	Stage             string    `json:"stage"`
	Nonce             string    `json:"nonce"`
	ReplayProfileID   string    `json:"replay_profile_id,omitempty"`
	CandidateA        string    `json:"candidate_a,omitempty"`
	TargetFingerprint string    `json:"target_fingerprint"`
	IssuedAt          time.Time `json:"issued_at"`
	ExpiresAt         time.Time `json:"expires_at"`
}

type MatrixWorkflowPublisher struct {
	baseURL   string
	token     string
	roomID    string
	managerID string
	secret    []byte
	client    *http.Client
}

type workflowStageDefinition struct {
	id     string
	title  string
	status string
}

var workflowAuthorityStages = []workflowStageDefinition{
	{id: "preview", title: "修复预演", status: "completed"},
	{id: "approval", title: "人工审批", status: "in_progress"},
	{id: "repair", title: "执行修复", status: "in_progress"},
	{id: "verify", title: "独立验证", status: "in_progress"},
	{id: "recovered", title: "恢复完成", status: "completed"},
}

func NewMatrixWorkflowPublisher(baseURL, token, roomID, managerID, secret string) (*MatrixWorkflowPublisher, error) {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(token) == "" ||
		!strings.HasPrefix(strings.TrimSpace(roomID), "!") ||
		!strings.HasPrefix(strings.TrimSpace(managerID), "@") || len(secret) < 16 {
		return nil, errors.New("workflow authority publisher configuration is incomplete")
	}
	return &MatrixWorkflowPublisher{
		baseURL: strings.TrimRight(baseURL, "/"), token: token,
		roomID: strings.TrimSpace(roomID), managerID: strings.TrimSpace(managerID),
		secret: []byte(secret), client: &http.Client{Timeout: workflowAuthorityTimeout},
	}, nil
}

func WorkflowPublisherFromEnv() (WorkflowPublisher, error) {
	return NewMatrixWorkflowPublisher(
		os.Getenv("AGENTTEAMS_MATRIX_URL"),
		os.Getenv("AGENTTEAMS_MANAGER_MATRIX_TOKEN"),
		os.Getenv("OPSKEEPER_DEMO_MATRIX_ROOM"),
		os.Getenv("OPSKEEPER_WORKFLOW_AUTHORITY_MANAGER_ID"),
		os.Getenv("OPSKEEPER_WORKFLOW_AUTHORITY_SECRET"),
	)
}

func (publisher *MatrixWorkflowPublisher) PublishWorkflow(
	ctx context.Context, run *demomodel.ScenarioRun, stage string, decision *PreviewDecisionSummary,
) error {
	if publisher == nil || run == nil {
		return errors.New("workflow authority publisher is not configured")
	}
	switch stage {
	case demomodel.ScenarioStatusPreviewReady, demomodel.ScenarioStatusAwaitingApproval,
		demomodel.ScenarioStatusRepairDispatched, demomodel.ScenarioStatusVerifying,
		demomodel.ScenarioStatusRecovered:
	default:
		return errors.New("unknown workflow authority stage")
	}
	now := time.Now().UTC()
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return err
	}
	claims := workflowAuthorityClaims{
		ManagerID: publisher.managerID, RoomID: publisher.roomID,
		IncidentID: strconv.FormatUint(run.IncidentID, 10), Stage: stage,
		Nonce: hex.EncodeToString(nonceBytes), TargetFingerprint: run.TargetFingerprint,
		IssuedAt: now, ExpiresAt: now.Add(30 * time.Second),
	}
	if decision != nil {
		claims.ReplayProfileID = decision.ReplayProfileID
		claims.CandidateA = decision.CandidateA
	}
	encodedClaims, err := json.Marshal(claims)
	if err != nil {
		return err
	}
	mac := hmac.New(sha256.New, publisher.secret)
	_, _ = mac.Write(encodedClaims)
	token := base64.RawURLEncoding.EncodeToString(encodedClaims) + "." + hex.EncodeToString(mac.Sum(nil))
	incidentID := claims.IncidentID
	utcTime := now.Format("2006-01-02T15:04:05Z")
	beijingTime := now.In(time.FixedZone("UTC+8", 8*60*60)).Format("2006-01-02T15:04:05+08:00")
	content := map[string]any{
		"msgtype": "m.notice",
		"body": fmt.Sprintf(
			"[OpsKeeper Authority] incident=%s stage=%s time_utc=%s time_bjt=%s\nOPSKEEPER_AUTHORITY_V1 %s",
			incidentID, stage, utcTime, beijingTime, token,
		),
		"agentteams.workflow": map[string]any{
			"type": "opskeeper-workflow", "runId": incidentID, "authorityStage": stage,
			"title":  "OpsKeeper 事故恢复 " + incidentID,
			"status": workflowStatus(stage), "source": "opskeeper-manager",
			"summary": workflowSummary(stage), "steps": workflowSteps(stage),
		},
		"opskeeper.authority": map[string]any{
			"version": 1, "manager_id": publisher.managerID, "incident_id": incidentID,
			"stage": stage, "token": token,
			"time_utc": utcTime, "time_bjt": beijingTime,
		},
	}
	encodedContent, err := json.Marshal(content)
	if err != nil {
		return err
	}
	eventURL := fmt.Sprintf(
		"%s/_matrix/client/v3/rooms/%s/send/m.room.message/%d-authority",
		publisher.baseURL, url.PathEscape(publisher.roomID), now.UnixNano(),
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, eventURL, bytes.NewReader(encodedContent))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+publisher.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := publisher.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<16))
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("matrix workflow authority send failed: %s", response.Status)
	}
	return nil
}

func workflowStatus(stage string) string {
	if stage == demomodel.ScenarioStatusRecovered {
		return "success"
	}
	return "in_progress"
}

func workflowSummary(stage string) string {
	switch stage {
	case demomodel.ScenarioStatusPreviewReady:
		return "修复预演完成，等待人工审批"
	case demomodel.ScenarioStatusAwaitingApproval:
		return "等待人工审批修复方案"
	case demomodel.ScenarioStatusRepairDispatched:
		return "修复方案已批准并派发执行"
	case demomodel.ScenarioStatusVerifying:
		return "修复执行完成，独立验证中"
	case demomodel.ScenarioStatusRecovered:
		return "修复验证通过，业务已恢复"
	default:
		return stage
	}
}

func workflowSteps(stage string) []map[string]any {
	selectedIndex := -1
	for index, definition := range workflowAuthorityStages {
		if definition.id == workflowStepID(stage) {
			selectedIndex = index
			break
		}
	}
	steps := make([]map[string]any, 0, len(workflowAuthorityStages)-1)
	for index, definition := range workflowAuthorityStages {
		if definition.id == "recovered" {
			continue
		}
		status := "pending"
		switch {
		case index < selectedIndex:
			status = "completed"
		case index == selectedIndex:
			status = definition.status
		case stage == demomodel.ScenarioStatusRecovered:
			status = "completed"
		}
		steps = append(steps, map[string]any{
			"id": definition.id, "name": definition.title, "status": status,
		})
	}
	return steps
}

func workflowStepID(stage string) string {
	switch stage {
	case demomodel.ScenarioStatusPreviewReady:
		return "preview"
	case demomodel.ScenarioStatusAwaitingApproval:
		return "approval"
	case demomodel.ScenarioStatusRepairDispatched:
		return "repair"
	case demomodel.ScenarioStatusVerifying, demomodel.ScenarioStatusRecovered:
		return "verify"
	default:
		return ""
	}
}
