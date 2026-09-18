// knowledge.js — 知识库贡献（引用 / 输出）的归一化与渲染辅助。
//
// 目标：在 RCA 报告中体现 OpsKeeper 知识库的贡献。
//   - 引用知识库：本次调查命中的 incident_pattern / postmortem 条目。
//   - 输出知识库：流程闭环（postmortem 阶段）写入 vault 的条目。
//
// 数据来源优先沿用既有端点：
//   - 引用：GET /api/opskeeper/knowledge/query?query=<rca summary>&top_k=5
//   - 输出：GET /api/opskeeper/incidents/<id>/archive 的 postmortem_refs
//
// 后端若未来把 kb_hits / knowledge_writes 直接放进 RootCauseJSON，render 层
// 会优先取这份权威数据，回退到上述独立调用，避免重复渲染。

function asArray(value) {
  return Array.isArray(value) ? value : [];
}

function asObject(value) {
  return value && typeof value === 'object' && !Array.isArray(value) ? value : null;
}

function pickString(source, keys) {
  if (!source) return '';
  for (const key of keys) {
    const value = source[key];
    if (value !== undefined && value !== null && value !== '') {
      return String(value);
    }
  }
  return '';
}

function pickNumber(source, keys) {
  if (!source) return null;
  for (const key of keys) {
    const value = source[key];
    if (typeof value === 'number' && Number.isFinite(value)) return value;
    if (typeof value === 'string' && value.trim() !== '' && !Number.isNaN(Number(value))) {
      return Number(value);
    }
  }
  return null;
}

// 归一化 KB 命中项。前端只关心稳定字段，避免被后端字段重命名影响。
export function normalizeKBHit(raw) {
  const hit = asObject(raw);
  if (!hit) return null;
  const doc = asObject(hit.doc) || hit;
  const id = pickString(doc, ['id', 'pattern_id', 'PatternID', 'doc_id']);
  const summary = pickString(doc, ['summary', 'Summary', 'title', 'root_cause', 'RootCause']);
  const resourceType = pickString(doc, ['resource_type', 'ResourceType', 'resourceType']);
  const symptom = pickString(doc, ['symptom', 'Symptom']);
  const rootCause = pickString(doc, ['root_cause', 'RootCause', 'rootCause']);
  const similarity = pickNumber(hit, ['similarity', 'Similarity', 'score'])
    ?? pickNumber(doc, ['similarity', 'Similarity', 'score']);
  const hitCount = pickNumber(doc, ['hit_count', 'HitCount', 'hitCount']);
  const postmortemId = pickString(doc, ['postmortem_id', 'PostmortemID', 'postmortemId']);
  const sourceType = pickString(doc, ['source_type', 'SourceType', 'sourceType']);
  const source = pickString(doc, ['source', 'origin'])
    || (resourceType ? `pattern:${resourceType}` : `knowledge:${sourceType || 'search'}`);
  if (!id && !summary && !rootCause && !symptom) return null;
  return {
    id: id || summary || rootCause || symptom,
    summary: summary || rootCause || symptom || '(无摘要)',
    symptom,
    rootCause,
    resourceType,
    similarity: similarity === null ? null : Math.max(0, Math.min(1, similarity)),
    hitCount: hitCount === null ? 0 : Math.max(0, hitCount),
    postmortemId,
    source,
  };
}

export function normalizeKBHitList(response) {
  if (Array.isArray(response)) {
    return response.map(normalizeKBHit).filter(Boolean);
  }
  const root = asObject(response);
  if (!root) return [];
  const candidates = [
    root.data?.hits,
    root.data?.results,
    root.data?.kb_hits,
    root.hits,
    root.results,
    root.kb_hits,
    root.data,
    root.items,
    root,
  ];
  for (const candidate of candidates) {
    if (Array.isArray(candidate)) {
      return candidate.map(normalizeKBHit).filter(Boolean);
    }
  }
  return [];
}

// 归一化 postmortem 输出条目（知识库产出）。后端字段名在不同版本间可能
// 不一致（snake/camel），这里做一次保守归一。
export function normalizePostmortemRef(raw) {
  const ref = asObject(raw);
  if (!ref) return null;
  const id = pickString(ref, ['id', 'doc_id', 'incident_id', 'postmortem_id']);
  const rootCause = pickString(ref, ['root_cause', 'rootCause', 'summary', 'title']);
  const confirmedBy = pickString(ref, ['confirmed_by', 'confirmedBy', 'author', 'user_id']);
  const confirmedAt = pickString(ref, ['confirmed_at', 'confirmedAt', 'written_at', 'updated_at']);
  if (!id && !rootCause) return null;
  return {
    id: id || rootCause || 'unknown',
    rootCause: rootCause || '(未记录根因)',
    confirmedBy,
    confirmedAt,
  };
}

export function normalizePostmortemRefList(response) {
  if (Array.isArray(response)) {
    return response.map(normalizePostmortemRef).filter(Boolean);
  }
  const root = asObject(response);
  if (!root) return [];
  const archive = asObject(root.data) || root;
  const refs = asArray(archive.postmortem_refs ?? archive.postmortemRefs ?? root.postmortem_refs);
  if (refs.length > 0) {
    return refs.map(normalizePostmortemRef).filter(Boolean);
  }
  return [];
}

export function extractKnowledgeFromEvidence(evidence) {
  return asArray(evidence).flatMap((raw) => {
    const item = asObject(raw);
    if (!item) return [];
    const domain = pickString(item, ['domain', 'Domain']).toLowerCase();
    const tool = pickString(item, ['tool', 'Tool']).toLowerCase();
    if (domain !== 'knowledge' && tool !== 'query_knowledge') return [];

    const summary = pickString(item, ['summary', 'title', 'snippet', 'ref', 'query']);
    const explicitId = pickString(item, ['id', 'pattern_id', 'PatternID', 'doc_id']);
    const step = item.step === undefined || item.step === null ? '' : String(item.step);
    const hit = normalizeKBHit({
      id: explicitId || (step && `evidence-${step}`) || summary,
      summary,
      symptom: pickString(item, ['symptom', 'Symptom']),
      root_cause: pickString(item, ['root_cause', 'RootCause', 'rootCause']),
      similarity: pickNumber(item, ['confidence', 'similarity', 'score']),
      hit_count: pickNumber(item, ['hit_count', 'HitCount', 'hitCount', 'count']),
      postmortem_id: pickString(item, ['postmortem_id', 'PostmortemID', 'postmortemId']),
      source: pickString(item, ['source', 'origin']) || (tool ? `rca-evidence:${tool}` : 'rca-evidence:knowledge'),
    });
    return hit ? [hit] : [];
  });
}

// 直接从 RCA 报告 payload（若后端已塞进 kb_hits / knowledge_writes）抽取。
export function extractKnowledgeFromReport(report) {
  const root = asObject(report);
  if (!root) return { hits: [], writes: [] };
  const data = asObject(root.data) || root;
  const rootObject = asObject(data.root_cause_object) || asObject(data.rootCauseObject);
  const evidence = [
    ...asArray(data.evidence_chain),
    ...asArray(data.evidence),
    ...asArray(rootObject?.evidence_chain),
    ...asArray(rootObject?.evidence),
  ];
  const directHits = normalizeKBHitList(data.kb_hits ?? data.knowledge_refs ?? data.knowledgeRefs);
  const evidenceHits = extractKnowledgeFromEvidence(evidence);
  const seen = new Set();
  const hits = [...directHits, ...evidenceHits].filter((hit) => {
    const key = hit.summary;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  return {
    hits,
    writes: normalizePostmortemRefList(data.knowledge_writes ?? data.knowledgeWrites ?? data.postmortem_refs),
  };
}

// 把相似度格式化成百分比；为 null 时返回 "—" 占位。
export function formatSimilarity(value) {
  if (value === null || value === undefined) return '—';
  return `${Math.round(value * 100)}%`;
}
