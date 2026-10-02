import { request } from './client';

// Plugin release client — talks to /v1/plugins/releases (admin only).
//
// The backend is core/manager/server/plugin/http.go, and the shapes
// mirror Go's release.Status. A release is a job that outlives the request
// that started it, so the console polls status and calls advance / halt /
// rollback separately rather than expecting one call to do all three.
//
// The one field worth reading carefully is `pending`: a node the wave has
// not heard from. It is NOT a failure — the wave cannot advance past it —
// and rendering the two the same way is how an operator ends up restarting
// a release that is simply waiting.

export type ReleaseNodeState =
  | 'installed'
  | 'refused'
  | 'failed'
  | 'pending'
  | (string & {});

export interface PluginInfo {
  name: string;
  version: string;
  digest?: string;
}

export interface ReleaseStatus {
  plugin: string;
  version: string;
  /** 1-based index of the wave currently out. */
  wave: number;
  /** Total waves the plan has. */
  waves: number;
  /** Nodes the current wave has not heard from. Blocks `advance`. */
  pending?: number[] | null;
  /** Nodes that answered with a failure. Counted as answered, so they do
   *  not block the wave — but they are why it moved early. */
  failed?: number[] | null;
  installed?: Record<string, PluginInfo> | null;
  summary?: string;
  halted?: boolean;
  rolled_back?: boolean;
  reason?: string;
}

export interface ReleaseList {
  items: ReleaseStatus[];
  total: number;
}

export interface StartReleaseRequest {
  plugin: string;
  version: string;
  url: string;
  sha256: string;
  signature: string;
  /** Optional; identifies which key in the node's trust store signed it. */
  key_id?: string;
  /** Required — the backend refuses to default it. A package the operator
   *  chose deliberately must not silently get a canary it never asked for,
   *  and a package that asked for one must not go out in a single wave
   *  because the field was empty. */
  strategy: 'rolling' | 'pin';
  /** Optional subset of edge ids. Empty means the whole fleet. */
  nodes?: number[];
}

export interface StartReleaseResponse extends ReleaseStatus {
  waves: number;
}

export interface AdvanceResponse extends ReleaseStatus {
  /** False when the current wave is not accounted for. The release did not
   *  move, and the audit trail records that as a failure. */
  moved: boolean;
}

export async function listReleases(): Promise<ReleaseStatus[]> {
  const r = await request<ReleaseList>('GET', '/plugins/releases');
  return r.items ?? [];
}

export async function startRelease(
  body: StartReleaseRequest
): Promise<StartReleaseResponse> {
  return request<StartReleaseResponse>('POST', '/plugins/releases', body);
}

export async function getRelease(name: string): Promise<ReleaseStatus> {
  return request<ReleaseStatus>('GET', `/plugins/releases/${encodeURIComponent(name)}`);
}

export async function advanceRelease(name: string): Promise<AdvanceResponse> {
  return request<AdvanceResponse>('POST', `/plugins/releases/${encodeURIComponent(name)}/advance`);
}

/** halt stops the release and LEAVES what is installed installed. Taking it
 *  back off is rollback, a separate call, because an operator who has just
 *  watched a canary go bad may want the release stopped now and the
 *  decision about the canary taken calmly. */
export async function haltRelease(name: string, reason: string): Promise<ReleaseStatus> {
  return request<ReleaseStatus>('POST', `/plugins/releases/${encodeURIComponent(name)}/halt`, {
    reason,
  });
}

export async function rollbackRelease(name: string): Promise<ReleaseStatus> {
  return request<ReleaseStatus>('POST', `/plugins/releases/${encodeURIComponent(name)}/rollback`);
}
