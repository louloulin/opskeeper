// getVersionDeployment — pulls the rich deployment view
// (Manager / Worker / plugin / server composition,
// health, dependencies, one worked recovery example). Reuses the
// generic request client so auth + error handling stay consistent.
import { request } from './client';

export type ManagerVersion = string;

export interface PluginManifest {
  apiVersion?: string;
  kind?: string;
  id: string;
  name: string;
  version: string;
  description?: string;
  author?: string;
  entry?: Record<string, string>;
  dashboardVersion?: string;
  min_version?: string;
  extensionPoints?: string[];
  permissions?: string[];
  dependencies?: string[];
}

export interface SkillVersion {
  id: string;
  version: string;
  role: string;
  path: string;
}

export interface HealthSummary {
  overall: string;
  db?: string;
  prom?: string;
  logs?: string;
  traces?: string;
  llm?: string;
  note?: string;
}

export interface DeployDependency {
  name: string;
  configured: boolean;
  required: boolean;
  note?: string;
}

export interface TimelinePhaseLite {
  phase: string;
  phase_label: string;
  status: string;
  worker_role: string;
  skill_version: string;
  duration?: string;
  summary?: string;
}

export interface RecoveryOperation {
  incident_id: string;
  title: string;
  fault_family: string;
  phases_observed: number;
  phases_expected: number;
  recovery_signal: boolean;
  closed: boolean;
  duration_sec: number;
  audit_kinds: string[];
  evidence?: Record<string, string>;
  phases?: TimelinePhaseLite[];
}

export interface DeploymentResponse {
  manager_version: ManagerVersion;
  go_version: string;
  os_arch: string;
  build_at?: string;
  plugin: PluginManifest;
  skills: SkillVersion[];
  health: HealthSummary;
  dependencies: DeployDependency[];
  recovery_example: RecoveryOperation;
  generated_at: string;
}

export function getVersionDeployment() {
  return request<DeploymentResponse>('GET', '/version/deployment');
}
