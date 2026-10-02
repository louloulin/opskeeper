package loop

// The production ArgResolver: it reads the arguments a remediation acts on
// out of the evidence the investigation already recorded.
//
// Why this exists rather than defaults. A RemediationOption carries an
// action name and a resource locator ("pg:alert-17") — never the pid, role
// or session the action targets. The tempting bridge is a default: kill the
// oldest backend, pause the role that looks busiest. That is how "terminate
// the long transaction" silently becomes "terminate a transaction nobody
// chose", and it is irreversible.
//
// So every extractor here is evidence-backed, and every ambiguity is a
// refusal. When the investigation recorded one candidate the extractor
// returns it; when it recorded several it lists them and declines, because
// choosing between them is a judgement the evidence does not make.
//
// The map is deliberately small. An action with no entry returns (nil, nil),
// which leaves the invoker's own missing-argument refusal in charge — the
// same behaviour this code had before an implementation existed. Declaring
// an extractor that guesses would be worse than declaring none.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// EvidenceArgResolver resolves tool arguments from an incident's recorded
// evidence chain.
type EvidenceArgResolver struct {
	// Causes reads the RootCauseJSON for the incident. Required for any
	// action that has a declared extractor; a nil loader makes those
	// actions refuse with a reason instead of dispatching without
	// arguments.
	Causes RootCauseLoader
}

// evidenceArgExtractor turns evidence items into the arguments of one
// action. It receives the option as well, so an extractor may use the
// target; none currently does, and that is a fact about the evidence rather
// than a design preference.
type evidenceArgExtractor func(req RemediationRequest, evidence []EvidenceItem) (map[string]any, error)

// evidenceArgExtractors is the closed set of actions whose arguments are
// recoverable from evidence.
//
// Keyed by the exact tool name, like every other vocabulary in this package:
// an action that is not in this map is not resolved, and a name that drifts
// stops resolving rather than resolves wrongly.
var evidenceArgExtractors = map[string]evidenceArgExtractor{
	"pg.kill_session":     extractPGSessionPID,
	"pg.connection_pause": extractPGRole,
}

// Resolve implements ArgResolver.
func (r EvidenceArgResolver) Resolve(ctx context.Context, req RemediationRequest, spec ToolSpec) (map[string]any, error) {
	action := strings.TrimSpace(req.Option.Action)
	extract, ok := evidenceArgExtractors[action]
	if !ok {
		// Not declared: the invoker's missing-argument check already
		// speaks for this action, and it names the arguments rather
		// than the missing extractor, which is the more useful of the
		// two messages.
		return nil, nil
	}
	if len(spec.RequiredArgs) == 0 {
		// The tool wants nothing this resolver could supply.
		return nil, nil
	}
	if r.Causes == nil {
		return nil, errors.New("no root-cause loader is wired, so the evidence cannot be read")
	}
	rc, err := r.Causes.LoadRootCause(ctx, req.TenantID, req.IncidentID)
	if err != nil {
		return nil, fmt.Errorf("read the evidence for incident %s: %w", req.IncidentID, err)
	}
	if rc == nil {
		return nil, fmt.Errorf("incident %s has no recorded root cause, so %s has no evidence to act on",
			req.IncidentID, action)
	}
	return extract(req, rc.EvidenceChain)
}

var _ ArgResolver = EvidenceArgResolver{}

// ActionsResolvableFromEvidence is every action for which a production
// extractor is declared.
//
// It is exported because it is a claim about this build that nothing else
// can see: the evaluation gate lists the actions whose required arguments a
// RemediationOption cannot carry, and without this list it cannot tell the
// ones that have somewhere to get them (a pid, a role) from the ones that
// have nowhere at all (a pod name no evidence records). Both are refusals
// today, and they need different work.
func ActionsResolvableFromEvidence() []string {
	out := make([]string, 0, len(evidenceArgExtractors))
	for action := range evidenceArgExtractors {
		out = append(out, action)
	}
	sort.Strings(out)
	return out
}

// extractPGSessionPID returns the pid of the one session the evidence
// describes.
func extractPGSessionPID(_ RemediationRequest, evidence []EvidenceItem) (map[string]any, error) {
	rows := pgActivityRows(evidence)
	if len(rows) == 0 {
		return nil, errors.New("no pg_stat_activity evidence was recorded, so there is no session to terminate")
	}
	byPID := map[int64][]string{}
	for _, row := range rows {
		pid, ok := rowInt(row, "pid")
		if !ok || pid <= 0 {
			continue
		}
		byPID[pid] = append(byPID[pid], describeSession(row))
	}
	switch len(byPID) {
	case 0:
		return nil, fmt.Errorf("the evidence names %d session(s) but none carries a usable pid", len(rows))
	case 1:
		for pid := range byPID {
			return map[string]any{"pid": pid}, nil
		}
	}
	return nil, fmt.Errorf("the evidence describes %d sessions and does not say which one to terminate; "+
		"terminating one of them at random is not a remediation, so this is refused — candidates: %s",
		len(byPID), strings.Join(sortedPIDs(byPID), ", "))
}

// extractPGRole returns the role the evidence shows as the source of the
// load.
func extractPGRole(_ RemediationRequest, evidence []EvidenceItem) (map[string]any, error) {
	rows := pgActivityRows(evidence)
	if len(rows) == 0 {
		return nil, errors.New("no pg_stat_activity evidence was recorded, so there is no role to pause")
	}
	roles := map[string]struct{}{}
	for _, row := range rows {
		role, ok := rowString(row, "usename")
		if !ok || role == "" {
			continue
		}
		roles[role] = struct{}{}
	}
	switch len(roles) {
	case 0:
		return nil, fmt.Errorf("the evidence names %d session(s) but none carries a usename", len(rows))
	case 1:
		for role := range roles {
			return map[string]any{"role": role}, nil
		}
	}
	return nil, fmt.Errorf("the evidence spans %d roles (%s) and pausing all of them is not what was approved",
		len(roles), strings.Join(sortedStrings(roles), ", "))
}

// pgActivityRows pulls the pg_stat_activity rows out of an evidence chain.
//
// The tool label is not what selects them: the real PostgreSQL investigator
// writes "pg_stat_activity", the golden corpus names the same fact
// "pg.active_sessions", and a resolver keyed on either one would go blind
// the day the other is used. A row that carries a pid is an activity row,
// so that is the test.
func pgActivityRows(evidence []EvidenceItem) []map[string]any {
	var rows []map[string]any
	for _, item := range evidence {
		for _, row := range decodeRows(item.Value) {
			if _, ok := row["pid"]; ok {
				rows = append(rows, row)
			}
		}
	}
	return rows
}

// decodeRows accepts every shape an evidence value arrives in: the typed
// slice the investigator built, the []any a JSON round trip through the
// contract table produces, and a raw JSON string. The contract stores
// evidence as interface{}, so all three are real.
func decodeRows(value any) []map[string]any {
	switch v := value.(type) {
	case []map[string]any:
		return v
	case []any:
		rows := make([]map[string]any, 0, len(v))
		for _, entry := range v {
			if row, ok := entry.(map[string]any); ok {
				rows = append(rows, row)
			}
		}
		return rows
	case string:
		var rows []map[string]any
		if err := json.Unmarshal([]byte(v), &rows); err != nil {
			return nil
		}
		return rows
	case json.RawMessage:
		var rows []map[string]any
		if err := json.Unmarshal(v, &rows); err != nil {
			return nil
		}
		return rows
	default:
		return nil
	}
}

// rowInt reads an integer field. A JSON round trip turns every number into
// a float64, so a resolver that only accepted int would fail on exactly the
// path it was written for.
func rowInt(row map[string]any, key string) (int64, bool) {
	raw, ok := row[key]
	if !ok {
		return 0, false
	}
	switch v := raw.(type) {
	case int:
		return int64(v), true
	case int32:
		return int64(v), true
	case int64:
		return v, true
	case float64:
		if v != float64(int64(v)) {
			return 0, false
		}
		return int64(v), true
	case json.Number:
		i, err := v.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}

func rowString(row map[string]any, key string) (string, bool) {
	raw, ok := row[key]
	if !ok {
		return "", false
	}
	s, ok := raw.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(s), true
}

// describeSession renders one candidate for a refusal message. It names the
// fields an operator would use to decide between candidates, and it does not
// include the query text: evidence is sanitized before it is stored, but a
// refusal is printed in more places than the evidence is.
func describeSession(row map[string]any) string {
	pid, _ := rowInt(row, "pid")
	parts := []string{fmt.Sprintf("pid %d", pid)}
	if role, ok := rowString(row, "usename"); ok && role != "" {
		parts = append(parts, "user="+role)
	}
	if app, ok := rowString(row, "application_name"); ok && app != "" {
		parts = append(parts, "app="+app)
	}
	if state, ok := rowString(row, "state"); ok && state != "" {
		parts = append(parts, "state="+state)
	}
	return strings.Join(parts, " ")
}

func sortedPIDs(byPID map[int64][]string) []string {
	out := make([]string, 0, len(byPID))
	for pid, descriptors := range byPID {
		out = append(out, fmt.Sprintf("pid %d (%s)", pid, strings.Join(descriptors, "; ")))
	}
	sort.Strings(out)
	return out
}

func sortedStrings(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for s := range set {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}
