export const BUSINESS_SECTIONS = ['orders', 'inventory', 'audit'] as const;

export type BusinessSection = (typeof BUSINESS_SECTIONS)[number];

export type BusinessSnapshot = {
  section: BusinessSection;
  value: string;
  detail: string;
  latency_ms: number;
  generated_at: string;
};

export const SCENARIO_STAGES = [
  'starting',
  'awaiting_alert',
  'alert_correlated',
  'diagnosis_dispatched',
  'preview_ready',
  'awaiting_approval',
  'repair_dispatched',
  'verifying',
  'recovered',
  'closed',
] as const;

export type WorkflowStage =
  | (typeof SCENARIO_STAGES)[number]
  | 'start_failed';

export type ScenarioStatus = {
  incident_id: number;
  scenario_id: string;
  status: WorkflowStage;
  pool_manifest_id: string;
  target_fingerprint: string;
  alert_fingerprint: string;
  updated_at: string;
  preview_decision?: PreviewDecisionSummary;
};

export type PreviewDecisionSummary = {
  replay_profile_id: string;
  boundary_text: string;
  candidate_a: string;
  candidate_b: string;
  eligible_for_hitl: boolean;
};

export type PreviewMetrics = {
  replay_profile_id: string;
  latency_p95_ms: number;
  throughput_rps: number;
  write_impact_pct: number;
  storage_delta_mb: number;
  consistency_checksum: string;
};

export type PreviewDecision = {
  replay_profile_id: string;
  boundary: string;
  baseline: PreviewMetrics;
  candidates: Array<{
    id: 'A' | 'B';
    decision: 'PASS' | 'FAIL';
    metrics: PreviewMetrics;
  }>;
};
