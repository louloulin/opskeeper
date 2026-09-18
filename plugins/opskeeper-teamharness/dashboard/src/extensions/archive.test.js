import assert from 'node:assert/strict';
import test from 'node:test';

import { normalizeArchiveIncidentList, normalizeArchiveResponse } from './archive.js';

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

test('normalizes archive incident options', () => {
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
