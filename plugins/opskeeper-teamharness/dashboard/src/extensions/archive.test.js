import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import test from 'node:test';

import {
  projectArchiveReplay,
  normalizeArchiveIncidentList,
  normalizeArchiveResponse,
  normalizeIncidentSummary,
  normalizeRepairPreviews,
  normalizeRepairPreviewSummary,
} from './archive.js';

const replayArchive = {
  incident_id: 'inc-replay',
  evidence_complete: true,
  recovery_observed: true,
  closed: true,
  event_count: 7,
  first_event_at: '2026-09-18T01:00:00Z',
  last_event_at: '2026-09-18T01:05:00Z',
  localization_seconds: 90,
  recovery_seconds: 60,
  timeline: [
    {
      id: 'event-closed', occurred_at: '2026-09-18T01:05:00Z', phase: 'closure',
      event_type: 'incident.closed', actor_type: 'system', actor: 'manager',
      status: 'closed', evidence_ref: 'evidence/closed',
    },
    {
      id: 'event-recovery', occurred_at: '2026-09-18T01:04:00Z', phase: 'recovery',
      event_type: 'recovery_signal.observed', actor_type: 'system', actor: 'verifier',
      status: 'verified', evidence_ref: 'evidence/recovery',
    },
    {
      id: 'event-action', occurred_at: '2026-09-18T01:03:00Z', phase: 'recovery',
      event_type: 'action.executed', actor_type: 'agent', actor: 'opskeeper-manager',
      status: 'executed', action_fingerprint: 'action-fingerprint', evidence_ref: 'evidence/action',
    },
    {
      id: 'event-approved', occurred_at: '2026-09-18T01:02:00Z', phase: 'approval',
      event_type: 'recommendation.approved', actor_type: 'human', actor: 'approver@example.test',
      status: 'approved', evidence_ref: 'evidence/approval',
    },
    {
      id: 'event-root-cause', occurred_at: '2026-09-18T01:01:30Z', phase: 'investigation',
      event_type: 'root_cause.confirmed', actor_type: 'agent', actor: 'investigator',
      status: 'confirmed', evidence_ref: 'evidence/root-cause',
    },
    {
      id: 'event-evidence', occurred_at: '2026-09-18T01:01:10Z', phase: 'investigation',
      event_type: 'evidence.refreshed', actor_type: 'system', actor: 'manager',
      status: 'refreshed', evidence_ref: 'evidence/refresh',
    },
    {
      id: 'event-alert', occurred_at: '2026-09-18T01:00:00Z', phase: 'detection',
      event_type: 'alert.received', actor_type: 'system', actor: 'prometheus',
      status: 'firing', evidence_ref: 'evidence/alert',
    },
  ],
  repair_previews: [{
    run_id: 'run-replay',
    target_fingerprint: 'sha256:target-v1',
    workload_fingerprint: 'sha256:workload-v1',
    seed_fingerprint: 'sha256:seed-v1',
    selected_candidate_id: 'candidate-a',
    candidates: [
      { candidate_id: 'baseline', name: 'Baseline', decision: 'BASELINE', average_latency_ms: 18 },
      {
        candidate_id: 'candidate-a', name: 'Candidate A', decision: 'PASS',
        average_latency_ms: 12, rejection_reason: '',
      },
      {
        candidate_id: 'candidate-b', name: 'Candidate B', decision: 'REJECTED_BY_PREVIEW',
        average_latency_ms: 25, rejection_reason: 'business probe failed',
      },
    ],
  }],
  rollback_plan: 'Restore pool configuration from pre-incident snapshot',
  rollback_result: 'succeeded',
  verification_result: 'passed',
  verification_criteria: 'pool latency returns below 30ms and business probe passes',
  postmortem_refs: [{
    id: 'postmortem-replay',
    root_cause: 'connection pool exhaustion',
    confirmed_by: 'reviewer@example.test',
    confirmed_at: '2026-09-19T01:00:00Z',
    source_event_ids: ['event-root-cause'],
  }],
  similar_incidents: Array.from({ length: 8 }, (_, index) => ({
    incident_id: `inc-similar-${index + 1}`,
    closed: true,
    similarity_score: 1 - index * 0.05,
    generated_at: '2026-09-19T02:00:00Z',
    source_event_ids: [`event-similar-${index + 1}`],
  })),
  similarity_limit: 3,
  similarity_source: 'tenant incident archive',
  controlled_drill: {
    supported: true,
    scenario_id: 'pg-pool-exhaustion',
    manifest_id: 'manifest-v1',
    target_fingerprint: 'sha256:target-v1',
    workload_fingerprint: 'sha256:workload-v1',
    safety_identity: 'sha256:safety-v1',
    action: { type: 'read_only', href: '/drills/pg-pool-exhaustion?incident=inc-replay' },
  },
};

test('projects chronological archive replay with frozen decision evidence and enrichment provenance', () => {
  const replay = projectArchiveReplay(replayArchive);

  assert.deepEqual(replay.timeline.map((event) => event.id), [
    'event-alert', 'event-evidence', 'event-root-cause', 'event-approved',
    'event-action', 'event-recovery', 'event-closed',
  ]);
  assert.deepEqual(replay.stageTimeline.map((stage) => stage.stage), [
    'detected', 'correlated', 'investigated', 'critiqued', 'approved', 'recovered', 'postmortem',
  ]);
  assert.equal(replay.stageTimeline.find((stage) => stage.stage === 'recovered').sourceEventId, 'event-recovery');
  assert.deepEqual(replay.timeline[0].evidenceRefs, ['evidence/alert']);
  assert.equal(replay.timeline[6].sourceEventId, 'event-closed');
  assert.equal(replay.closure.incidentId, 'inc-replay');
  assert.equal(replay.closure.closed, true);
  assert.equal(replay.completeness, 'complete');

  assert.equal(replay.candidateComparison.selected.id, 'candidate-a');
  assert.equal(replay.candidateComparison.rejected[0].id, 'candidate-b');
  assert.equal(replay.candidateComparison.rejected[0].reason, 'business probe failed');
  assert.equal(replay.rollback.result, 'succeeded');
  assert.deepEqual(replay.rollback.sourceEventIds, ['event-action']);
  assert.equal(replay.verification.result, 'passed');
  assert.deepEqual(replay.verification.sourceEventIds, ['event-recovery']);

  assert.equal(replay.decisionEvidence[0].provenance, 'decision_time');
  assert.ok(replay.decisionEvidence.every((item) => item.sourceEventIds.length > 0));
  assert.equal(replay.enrichment.postIncident[0].id, 'postmortem-replay');
  assert.equal(replay.enrichment.postIncident[0].provenance, 'post_incident_enrichment');
  assert.deepEqual(replay.enrichment.postIncident[0].sourceEventIds, ['event-root-cause']);
  assert.ok(!replay.decisionEvidence.some((item) => item.id === 'postmortem-replay'));
});

test('bounds archive similarities and exposes explicit comparison provenance', () => {
  const replay = projectArchiveReplay(replayArchive);

  assert.equal(replay.similarities.limit, 3);
  assert.equal(replay.similarities.total, 8);
  assert.equal(replay.similarities.items.length, 3);
  assert.equal(replay.similarities.truncated, true);
  assert.deepEqual(replay.similarities.items.map((item) => item.id), [
    'inc-similar-1', 'inc-similar-2', 'inc-similar-3',
  ]);
  assert.equal(replay.similarities.provenance.kind, 'post_incident_enrichment');
  assert.equal(replay.similarities.provenance.source, 'tenant incident archive');
  assert.deepEqual(replay.similarities.items[0].sourceEventIds, ['event-similar-1']);
});

test('marks legacy optional replay fields not applicable and never fabricates a drill', () => {
  const replay = projectArchiveReplay({
    incident_id: 'inc-legacy',
    closed: true,
    timeline: replayArchive.timeline.filter((event) => event.id !== 'event-closed').concat([{
      ...replayArchive.timeline[0],
      occurred_at: '2026-09-18T01:05:00Z',
    }]),
    repair_previews: [],
    postmortem_refs: [],
    similar_incidents: [],
  });

  assert.equal(replay.candidateComparison.completeness, 'legacy_not_applicable');
  assert.equal(replay.candidateComparison.selected, null);
  assert.deepEqual(replay.candidateComparison.rejected, []);
  assert.equal(replay.completeness, 'partial');
  assert.equal(replay.controlledDrill.supported, false);
  assert.equal(replay.controlledDrill.action, undefined);
});

test('hides a controlled drill when one identity is missing even if support is claimed', () => {
  const replay = projectArchiveReplay({
    ...replayArchive,
    controlled_drill: { ...replayArchive.controlled_drill, safety_identity: '' },
  });

  assert.equal(replay.controlledDrill.supported, false);
  assert.deepEqual(replay.controlledDrill.missingIdentities, ['safety']);
  assert.equal(replay.controlledDrill.action, undefined);
});

test('renders replay-first archive UI and hides unsupported controlled drills', () => {
  const source = readFileSync(fileURLToPath(new URL('./archive-route.jsx', import.meta.url)), 'utf8');
  const replaySummaryIndex = source.indexOf('<ArchiveReplaySummary');
  const replayTimelineIndex = source.indexOf('<StageTimeline');
  const sourceTableIndex = source.indexOf('aria-label="归档事件源"');
  const denseTableIndex = source.indexOf('<RepairPreviewArchive');

  assert.ok(replaySummaryIndex >= 0);
  assert.ok(replayTimelineIndex > replaySummaryIndex);
  assert.ok(sourceTableIndex > replayTimelineIndex);
  assert.ok(denseTableIndex > sourceTableIndex);
  assert.match(source, /projectArchiveReplay\(archive\)/u);
  assert.match(source, /replay\.controlledDrill\.supported &&/u);
  assert.match(source, /受控演练只读回看/u);
  assert.doesNotMatch(source, /createDrill|startDrill|mutation|method:\s*['`]POST['`]/iu);
});

test('normalizes archive response wrappers and arrays', () => {
  const archive = normalizeArchiveResponse({
    code: 0,
    data: {
      incident_id: 'inc-1',
      evidence_complete: true,
      timeline: [{ id: 'event-1' }],
    },
  });

  assert.equal(archive.incident_id, 'inc-1');
  assert.equal(archive.evidence_complete, true);
  assert.deepEqual(archive.trace_ids, []);
  assert.deepEqual(archive.similar_incidents, []);
  assert.deepEqual(archive.postmortem_refs, []);
  assert.deepEqual(archive.repair_previews, []);
});

test('projects authoritative repair previews responsively without replacing them with the preview link', () => {
  const source = readFileSync(fileURLToPath(new URL('./archive-route.jsx', import.meta.url)), 'utf8');

  assert.match(source, /import \{ formatBeijingTime as formatTime \} from '\.\/time-format\.js';/u);
  assert.match(source, /RepairPreviewArchive runs=\{archive\.repair_previews\}/u);
  assert.match(source, /Manager Archive 权威数据 · 只读投影/u);
  assert.match(source, /辅助深链：preview-pg/u);
  assert.match(source, /暂无修复预演记录/u);
  assert.match(source, /overflowX:\s*'auto'/u);
  assert.match(source, /tableLayout:\s*'fixed'/u);
  assert.match(source, /overflowWrap:\s*'anywhere'/u);
  assert.match(source, /normalized === 'PASS' \? '#16a34a'/u);
  assert.match(source, /normalized === 'REJECTED_BY_PREVIEW' \? '#dc2626'/u);
  assert.match(source, /normalized === 'FAIL' \? '#d97706'/u);
});

test('projects the compact approval gate after RCA and preserves the controlled-load boundary', () => {
  const source = readFileSync(fileURLToPath(new URL('./route.jsx', import.meta.url)), 'utf8');
  const knowledgeIndex = source.indexOf('<KnowledgePanel');
  const gateIndex = source.indexOf('<RepairPreviewGate');
  const rawJsonIndex = source.indexOf('{/* Raw JSON fallback */}');

  assert.ok(knowledgeIndex >= 0);
  assert.ok(gateIndex > knowledgeIndex);
  assert.ok(rawJsonIndex > gateIndex);
  assert.match(source, /String\(i\.id \?\? ''\)\.includes\(filter\)/u);
  assert.match(source, /String\(i\.summary \?\? ''\)\.includes\(filter\)/u);
  assert.match(source, /opskeeperApi\.getIncidentRepairPreviewSummary\(incidentId\)/u);
  assert.match(source, /Controlled fixed-workload reconstruction in disposable preview-pg; original active sessions are not copied\./u);
  assert.match(source, /PASS \/ eligible for human approval/u);
  assert.match(source, /blocked before human approval/u);
  assert.match(source, /PASS 仅代表预演资格通过，人工审批前不改变生产数据。/u);
});

test('normalizes repair preview wrappers and candidate arrays without mutation', () => {
  const response = {
    data: {
      repair_previews: [
        {
          run_id: 'run-1',
          workload_fingerprint: 'sha256:workload-v1',
          seed_fingerprint: 'sha256:seed-v1',
          isolation_boundary: 'Controlled fixed-workload reconstruction in disposable preview-pg; original active sessions are not copied.',
          candidates: [{
            candidate_id: 'baseline', name: 'Baseline replay', consistent: true,
            average_latency_ms: 18, p95_latency_ms: 29, tps: 120, write_impact: 'none',
            storage_delta_bytes: 0, business_probe_pass: true, decision: 'PASS',
          }],
        },
        { run_id: 'run-2', candidates: null },
      ],
    },
  };

  const previews = normalizeRepairPreviews(response);

  assert.equal(previews[0].workloadFingerprint, undefined);
  assert.equal(previews[0].candidates[0].candidate_id, 'baseline');
  assert.equal(previews[0].candidates[0].average_latency_ms, 18);
  assert.equal(previews[0].candidates[0].p95_latency_ms, 29);
  assert.equal(previews[0].candidates[0].write_impact, 'none');
  assert.equal(previews[0].candidates[0].storage_delta_bytes, 0);
  assert.equal(previews[0].candidates[0].business_probe_pass, true);
  assert.deepEqual(previews[1].candidates, []);
  assert.equal(response.data.repair_previews[1].candidates, null);
  assert.deepEqual(normalizeRepairPreviews({ data: { repair_previews: [{ candidates: [] }] } }), []);
});

test('normalizes compact repair preview summary and keeps the legacy empty state', () => {
  const summary = normalizeRepairPreviewSummary({
    data: {
      incident_id: 'inc-1',
      run_id: 'run-1',
      seed_fingerprint: 'sha256:seed-v1',
      workload_fingerprint: 'sha256:workload-v1',
      controlled_load: true,
      isolation_boundary: 'Controlled fixed-workload reconstruction in disposable preview-pg; original active sessions are not copied.',
      baseline: {
        candidate_id: 'baseline', average_latency_ms: 18, write_impact: 'none',
        storage_delta_bytes: 0, business_probe_pass: true, decision: 'PASS',
      },
      passing: {
        candidate_id: 'candidate-a', average_latency_ms: 12, p95_latency_ms: 20,
        write_impact: 'preview_only', storage_delta_bytes: 1024, business_probe_pass: true,
        decision: 'PASS',
      },
      rejected: {
        candidate_id: 'candidate-b', average_latency_ms: 25, p95_latency_ms: 42,
        business_probe_pass: false, decision: 'REJECTED_BY_PREVIEW', rejection_reason: 'business probe failed',
      },
    },
  });

  assert.equal(summary.runId, 'run-1');
  assert.equal(summary.seedFingerprint, 'sha256:seed-v1');
  assert.equal(summary.workloadFingerprint, 'sha256:workload-v1');
  assert.equal(summary.baseline.average_latency_ms, 18);
  assert.equal(summary.baseline.write_impact, 'none');
  assert.notEqual(summary.baseline.candidate_id, summary.passing.candidate_id);
  assert.equal(summary.passing.decision, 'PASS');
  assert.equal(summary.rejected.decision, 'REJECTED_BY_PREVIEW');
  assert.equal(summary.rejected.rejection_reason, 'business probe failed');
  assert.match(summary.isolationBoundary, /original active sessions are not copied/);
  assert.deepEqual(normalizeRepairPreviewSummary(null), {
    incidentId: '', runId: '', seedFingerprint: '', workloadFingerprint: '',
    controlledLoad: false, isolationBoundary: '', baseline: null, passing: null, rejected: null,
  });
});

test('rejects an invalid archive response', () => {
  assert.equal(normalizeArchiveResponse({ code: 0 }), null);
  assert.equal(normalizeArchiveResponse(null), null);
});

test('normalizes archive index items by incident_id and closed flag', () => {
  const incidents = normalizeArchiveIncidentList({
    items: [
      { incident_id: 'inc-closed', event_count: 7, evidence_complete: true, closed: true },
      { incident_id: '', event_count: 1 },
    ],
  });

  assert.deepEqual(incidents, [
    {
      id: 'inc-closed',
      summary: 'inc-closed',
      status: 'closed',
      evidenceComplete: true,
      eventCount: 7,
    },
  ]);
});

test('normalizes live incident list items by numeric id and summary/rule_key/status', () => {
  const incidents = normalizeArchiveIncidentList({
    items: [
      {
        id: 38,
        rule_key: 'scrape_down',
        summary: 'scrape_down: up == 0 ⇒ instance=opskeeper-demo-node-metrics:8095',
        event_count: 1,
        status: 'resolved',
      },
      {
        id: 34,
        rule_key: 'pool_exhausted',
        summary: 'PostgreSQL 应用连接池耗尽',
        event_count: 1,
        status: 'resolved',
      },
    ],
  });

  assert.equal(incidents.length, 2);
  assert.equal(incidents[0].id, '38');
  assert.match(incidents[0].summary, /scrape_down/);
  assert.equal(incidents[0].status, 'resolved');
  assert.equal(incidents[0].evidenceComplete, false);
  assert.equal(incidents[0].eventCount, 1);
  assert.equal(incidents[1].id, '34');
  assert.equal(incidents[1].summary, 'PostgreSQL 应用连接池耗尽');
});

test('normalizes an incident summary response with labels and timeline', () => {
  const summary = normalizeIncidentSummary({
    code: 0,
    data: {
      id: 34,
      rule_key: 'pool_exhausted',
      rule_name: 'Pool Exhausted',
      severity: 'critical',
      status: 'resolved',
      summary: 'PostgreSQL 应用连接池耗尽',
      event_count: 1,
      target_type: 'pg_pool',
      dedupe_key: 'pipeline:pool_exhausted:foo',
      labels: { instance: 'pg-primary:5432', job: 'pgpool' },
      fired_at: '2026-09-18T01:00:00Z',
      resolved_at: '2026-09-18T01:05:00Z',
      value: 100,
    },
  });

  assert.equal(summary.id, '34');
  assert.equal(summary.ruleKey, 'pool_exhausted');
  assert.equal(summary.status, 'resolved');
  assert.equal(summary.severity, 'critical');
  assert.deepEqual(summary.labels, { instance: 'pg-primary:5432', job: 'pgpool' });
  assert.equal(summary.firedAt, '2026-09-18T01:00:00Z');
  assert.equal(summary.value, 100);
});

test('rejects an invalid incident summary response', () => {
  assert.equal(normalizeIncidentSummary({ code: 0 }), null);
  assert.equal(normalizeIncidentSummary(null), null);
});

test('keeps the archive layout responsive and safely wraps long values', () => {
  const source = readFileSync(new URL('./archive-route.jsx', import.meta.url), 'utf8');

  assert.match(source, /minmax\(min\(100%,\s*180px\),\s*1fr\)/u);
  assert.match(source, /minmax\(min\(100%,\s*360px\),\s*1fr\)/u);
  assert.match(source, /overflowWrap:\s*['`]anywhere['`]/u);
  assert.doesNotMatch(source, /repeat\(4,\s*minmax\(160px,\s*1fr\)\)/u);
  assert.doesNotMatch(source, /minWidth:\s*190/u);
});
