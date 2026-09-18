import assert from 'node:assert/strict';
import test from 'node:test';

import {
  extractKnowledgeFromReport,
  formatSimilarity,
  normalizeKBHit,
  normalizeKBHitList,
  normalizePostmortemRef,
  normalizePostmortemRefList,
} from './knowledge.js';

test('normalizeKBHit fills summary from rootCause when summary missing', () => {
  const hit = normalizeKBHit({
    pattern_id: 17,
    resource_type: 'pg',
    root_cause: 'pg long running transaction',
    similarity: '0.92',
    hit_count: 5,
    postmortem_id: 'pm-17',
  });
  assert.equal(hit.id, '17');
  assert.equal(hit.summary, 'pg long running transaction');
  assert.equal(hit.similarity, 0.92);
  assert.equal(hit.hitCount, 5);
  assert.equal(hit.postmortemId, 'pm-17');
  assert.equal(hit.source, 'pattern:pg');
});

test('normalizeKBHit rejects empty payloads', () => {
  assert.equal(normalizeKBHit(null), null);
  assert.equal(normalizeKBHit({}), null);
  assert.equal(normalizeKBHit({ foo: 'bar' }), null);
});

test('normalizeKBHit clamps out-of-range similarity', () => {
  const hit = normalizeKBHit({ id: 'a', summary: 'x', similarity: 2.4 });
  assert.equal(hit.similarity, 1);
  const neg = normalizeKBHit({ id: 'b', summary: 'y', similarity: -0.3 });
  assert.equal(neg.similarity, 0);
});

test('normalizeKBHitList unwraps common envelopes', () => {
  const wrapped = normalizeKBHitList({ data: { hits: [{ id: '1', summary: 's1' }] } });
  assert.equal(wrapped.length, 1);
  assert.equal(wrapped[0].id, '1');

  const bare = normalizeKBHitList([{ id: '2', summary: 's2' }]);
  assert.equal(bare.length, 1);
  assert.equal(bare[0].id, '2');

  const nested = normalizeKBHitList({ data: { kb_hits: [{ PatternID: 5, RootCause: 'rc' }] } });
  assert.equal(nested.length, 1);
  assert.equal(nested[0].summary, 'rc');

  assert.deepEqual(normalizeKBHitList(null), []);
});

test('normalizePostmortemRef flattens snake/camel variants', () => {
  const ref = normalizePostmortemRef({
    id: 'pm-1',
    root_cause: 'pg lock',
    confirmed_by: 'reviewer',
    confirmed_at: '2026-09-15T08:00:00Z',
  });
  assert.equal(ref.id, 'pm-1');
  assert.equal(ref.rootCause, 'pg lock');
  assert.equal(ref.confirmedBy, 'reviewer');
  assert.equal(ref.confirmedAt, '2026-09-15T08:00:00Z');

  const camel = normalizePostmortemRef({
    id: 'pm-2',
    rootCause: 'redis oom',
    confirmedBy: 'auto',
    confirmedAt: '2026-09-15T09:00:00Z',
  });
  assert.equal(camel.rootCause, 'redis oom');
  assert.equal(camel.confirmedBy, 'auto');

  assert.equal(normalizePostmortemRef(null), null);
  assert.equal(normalizePostmortemRef({}), null);
});

test('normalizePostmortemRefList drills into data envelope', () => {
  const list = normalizePostmortemRefList({
    data: { postmortem_refs: [{ id: 'a', root_cause: 'rc-a' }] },
  });
  assert.equal(list.length, 1);
  assert.equal(list[0].id, 'a');

  assert.deepEqual(normalizePostmortemRefList({ data: { postmortem_refs: [] } }), []);
  assert.deepEqual(normalizePostmortemRefList(null), []);
});

test('extractKnowledgeFromReport prefers embedded kb_hits and knowledge_writes', () => {
  const out = extractKnowledgeFromReport({
    kb_hits: [{ id: 'k1', summary: 'kb-1' }],
    knowledge_writes: [{ id: 'w1', root_cause: 'rc' }],
  });
  assert.equal(out.hits.length, 1);
  assert.equal(out.hits[0].id, 'k1');
  assert.equal(out.writes.length, 1);
  assert.equal(out.writes[0].id, 'w1');
});

test('extractKnowledgeFromReport returns empty arrays for legacy payloads', () => {
  const out = extractKnowledgeFromReport({
    schema_version: 'v1',
    root_cause_object: { kind: 'pg_long_tx' },
    evidence_chain: [],
  });
  assert.deepEqual(out.hits, []);
  assert.deepEqual(out.writes, []);
});

test('formatSimilarity renders percent and placeholder', () => {
  assert.equal(formatSimilarity(0.875), '88%');
  assert.equal(formatSimilarity(0), '0%');
  assert.equal(formatSimilarity(null), '—');
  assert.equal(formatSimilarity(undefined), '—');
});
