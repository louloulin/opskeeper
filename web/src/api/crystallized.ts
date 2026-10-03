// 结晶模式（cost crystallisation）审查面 —— `/v1/loops/crystallized`。
//
// 后端把 (fault, fix) 模式在连续多次「一次通过」的验证后晋升为一份草稿包；
// 这个模块只是把那份模式读出来给审批人看。它刻意只有读 + 落盘两件事：
//
//   - list / detail 渲染「平台准备在没有模型介入的情况下自己跑哪条命令」，
//     包括逐词的 argv 和晋升所依据的运行 id；
//   - promote 把草稿写进 manager 的评审根（OPSKEEPER_PLUGIN_IMPORT_DIR），
//     它 **不安装**。包仍要走和所有包一样的审核与签名通道。
//
// 因此这里没有 approve：审批发生在 plugin release 控制台，不在这个 API。
import { request } from './client';

export interface CrystallizedTrigger {
  kind: string;
  metric?: string;
  threshold?: number;
}

export interface CrystallizedPattern {
  name: string;
  action: string;
  fault_kind: string;
  family?: string;
  tool: string;
  class: string;
  argv: string[];
  target: string;
  trigger: CrystallizedTrigger;
  blast_radius: string;
  ttl_seconds: number;
  safety_level: string;
  streak: number;
  verified: number;
  attempts: number;
  rejections: number;
  first_seen: string;
  last_seen: string;
  promoted_at: string;
  evidence?: string[];
}

export interface CrystallizedPolicy {
  min_clean_streak: number;
  max_ttl_seconds: number;
  package_prefix: string;
  version: string;
  vendor: string;
}

export interface CrystallizedListResponse {
  items: CrystallizedPattern[];
  total: number;
  policy: CrystallizedPolicy;
}

export interface CrystallizedDetailResponse {
  pattern: CrystallizedPattern;
  yaml: string;
}

export interface CrystallizedPromoteResponse {
  name: string;
  dir: string;
}

export function listCrystallized(signal?: AbortSignal): Promise<CrystallizedListResponse> {
  return request<CrystallizedListResponse>('GET', '/loops/crystallized', undefined, { signal });
}

export function getCrystallized(name: string, signal?: AbortSignal): Promise<CrystallizedDetailResponse> {
  return request<CrystallizedDetailResponse>(
    'GET',
    `/loops/crystallized/${encodeURIComponent(name)}`,
    undefined,
    { signal }
  );
}

export function promoteCrystallized(name: string): Promise<CrystallizedPromoteResponse> {
  return request<CrystallizedPromoteResponse>(
    'POST',
    `/loops/crystallized/${encodeURIComponent(name)}/promote`
  );
}
