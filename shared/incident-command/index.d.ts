export type CommandPhase =
  | 'detected'
  | 'correlated'
  | 'investigated'
  | 'critiqued'
  | 'approved'
  | 'recovered'
  | 'postmortem';

export type StageStatus = 'pending' | 'running' | 'blocked' | 'completed' | 'failed' | 'unknown';
export type StageSubstate = 'awaiting_human' | 'approved' | 'executing' | 'verifying';
export type Freshness = 'fresh' | 'stale' | 'unknown';
export type Completeness = 'complete' | 'partial' | 'missing' | 'legacy_not_applicable';

export interface IncidentCommandOwner {
  kind: 'manager' | 'worker' | 'human' | 'verifier' | 'system';
  role?: string;
  label: string;
}

export interface IncidentCommandView {
  incidentId: string;
  scenario?: string;
  stage?: CommandPhase;
  stageStatus: StageStatus;
  stageSubstate?: StageSubstate;
  freshness: Freshness;
  observedAt?: string;
  serverNow?: string;
  elapsedMs?: number;
  sourceEventId?: string;
  sourceTaskId?: string;
  owner?: IncidentCommandOwner;
  businessImpact: {
    level: 'unknown' | 'normal' | 'degraded' | 'severe';
    affectedScopes: Array<{ name: string; state: 'healthy' | 'degraded' | 'unknown' }>;
    indicators: Array<{ name: string; value?: string; state: 'healthy' | 'degraded' | 'unknown' }>;
  };
  nextAction?: {
    kind: 'wait' | 'inspect-evidence' | 'approve' | 'reject' | 'verify' | 'archive' | 'retry';
    priority: number;
    label: string;
    detail?: string;
    disabledReason?: string;
  };
  stageTimeline: Array<{
    stage: CommandPhase;
    status: StageStatus;
    ownerLabel?: string;
    startedAt?: string;
    durationMs?: number;
    outcome?: string;
    blockingReason?: string;
    evidenceRefs: string[];
    sourceEventId?: string;
    sourceTaskId?: string;
  }>;
  evidenceCompleteness: Record<
    'incident' | 'cause' | 'preview' | 'authorization' | 'execution' | 'verification',
    Completeness
  >;
}

export const COMMAND_PHASES: readonly CommandPhase[];
export const COMMAND_PHASE_LABELS: Readonly<Record<CommandPhase, string>>;
export function fromManagerLoop(input?: unknown): IncidentCommandView;
export function fromDemoScenario(input?: unknown): IncidentCommandView;
export function resolveFreshness(observedAt?: string, serverNow?: string): Freshness;
export function selectNextAction(input?: unknown): IncidentCommandView['nextAction'];
export const emptyManagerLoop: IncidentCommandView;
export const managerApprovedPause: IncidentCommandView;
export const demoAwaitingApproval: IncidentCommandView;
