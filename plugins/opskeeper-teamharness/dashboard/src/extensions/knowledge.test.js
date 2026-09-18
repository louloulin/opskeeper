import assert from 'node:assert/strict';
import test from 'node:test';

import {
  extractKnowledgeFromEvidence,
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

  const searched = normalizeKBHitList({ items: [{ doc: { id: '9', title: 'runbook' }, score: 0.73 }] });
  assert.equal(searched.length, 1);
  assert.equal(searched[0].summary, 'runbook');
  assert.equal(searched[0].similarity, 0.73);

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

test('extractKnowledgeFromEvidence maps RCA knowledge evidence to KB hits', () => {
  const hits = extractKnowledgeFromEvidence([
    { step: 4, domain: 'knowledge', tool: 'query_knowledge', summary: 'known regression runbook', confidence: 0.82 },
    { step: 5, domain: 'middleware', tool: 'pg.query', summary: 'active transaction' },
    { step: 6, tool: 'query_knowledge', summary: 'pool exhaustion postmortem', confidence: 0.71 },
  ]);

  assert.equal(hits.length, 2);
  assert.equal(hits[0].id, 'evidence-4');
  assert.equal(hits[0].summary, 'known regression runbook');
  assert.equal(hits[0].similarity, 0.82);
  assert.equal(hits[0].source, 'rca-evidence:query_knowledge');
  assert.equal(hits[1].id, 'evidence-6');
});

test('extractKnowledgeFromReport merges direct and evidence hits without duplicates', () => {
  const out = extractKnowledgeFromReport({
    kb_hits: [{ id: 'k1', summary: 'kb-1' }],
    evidence_chain: [
      { step: 1, domain: 'knowledge', summary: 'kb-1' },
      { step: 2, tool: 'query_knowledge', summary: 'kb-2', confidence: 0.64 },
    ],
  });

  assert.deepEqual(out.hits.map((hit) => hit.summary), ['kb-1', 'kb-2']);
  assert.equal(out.hits[1].source, 'rca-evidence:query_knowledge');
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
