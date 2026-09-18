import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import test from 'node:test';

import {
  normalizeArchiveIncidentList,
  normalizeArchiveResponse,
  normalizeIncidentSummary,
} from './archive.js';

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
