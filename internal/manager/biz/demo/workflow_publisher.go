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
	content := map[string]any{
		"msgtype": "m.notice",
		"body": fmt.Sprintf(
			"[OpsKeeper Authority] incident=%s stage=%s\nOPSKEEPER_AUTHORITY_V1 %s",
			incidentID, stage, token,
		),
		"agentteams.workflow": map[string]any{
			"type": "opskeeper-workflow", "runId": incidentID, "authorityStage": stage,
			"status": "manager_authority", "source": "opskeeper-manager",
		},
		"opskeeper.authority": map[string]any{
			"version": 1, "manager_id": publisher.managerID, "incident_id": incidentID,
			"stage": stage, "token": token,
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
