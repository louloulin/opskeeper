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
  const id = pickString(hit, ['id', 'pattern_id', 'PatternID', 'doc_id']);
  const summary = pickString(hit, ['summary', 'Summary', 'title', 'root_cause', 'RootCause']);
  const resourceType = pickString(hit, ['resource_type', 'ResourceType', 'resourceType']);
  const symptom = pickString(hit, ['symptom', 'Symptom']);
  const rootCause = pickString(hit, ['root_cause', 'RootCause', 'rootCause']);
  const similarity = pickNumber(hit, ['similarity', 'Similarity', 'score']);
  const hitCount = pickNumber(hit, ['hit_count', 'HitCount', 'hitCount']);
  const postmortemId = pickString(hit, ['postmortem_id', 'PostmortemID', 'postmortemId']);
  const source = pickString(hit, ['source', 'origin']) || (resourceType ? `pattern:${resourceType}` : 'pattern');
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

// 直接从 RCA 报告 payload（若后端已塞进 kb_hits / knowledge_writes）抽取。
export function extractKnowledgeFromReport(report) {
  const root = asObject(report);
  if (!root) return { hits: [], writes: [] };
  const data = asObject(root.data) || root;
  return {
    hits: normalizeKBHitList(data.kb_hits ?? data.knowledge_refs ?? data.knowledgeRefs),
    writes: normalizePostmortemRefList(data.knowledge_writes ?? data.knowledgeWrites ?? data.postmortem_refs),
  };
}

// 把相似度格式化成百分比；为 null 时返回 "—" 占位。
export function formatSimilarity(value) {
  if (value === null || value === undefined) return '—';
  return `${Math.round(value * 100)}%`;
}
