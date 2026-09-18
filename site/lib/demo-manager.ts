import type {
  BusinessSection,
  BusinessSnapshot,
  ScenarioStatus,
} from '@/lib/demo-types';

const requestTimeoutMs = 2_000;

type ManagerEnvelope<T> = {
  code: number;
  message: string;
  data: T;
};

type ManagerErrorEnvelope = {
  code?: string;
  message?: string;
  error_code?: string;
};

export class DemoManagerError extends Error {
  readonly status: number;
  readonly code: string;

  constructor(code: string, message: string, status = 500) {
    super(message);
    this.name = 'DemoManagerError';
    this.status = status;
    this.code = code;
  }
}

type StartScenarioInput = {
  idempotency_key: string;
  scenario_id: string;
  target: string;
  target_fingerprint: string;
  alert_fingerprint: string;
  duration_seconds: number;
};

export const FINAL_DEMO_CONFIG = {
  scenarioId: 'pg-pool-exhaustion',
  target: 'pg:pool-fixture',
  targetFingerprint: '0123456789abcdef0123456789abcdef',
  alertFingerprint: 'fedcba9876543210fedcba9876543210',
  durationSeconds: 90,
} as const;

function configuration() {
  const managerUrl = process.env.OPSKEEPER_MANAGER_URL?.trim();
  const managerToken = process.env.OPSKEEPER_DEMO_API_TOKEN?.trim();

  if (!managerUrl || !managerToken) {
    throw new DemoManagerError(
      'manager_not_configured',
      'OpsKeeper Manager demo API configuration is incomplete',
    );
  }

  return { managerUrl, managerToken };
}

async function requestManager<T>(path: string, init?: RequestInit): Promise<T> {
  const { managerUrl, managerToken } = configuration();

  let response: Response;
  try {
    response = await fetch(new URL(path, managerUrl), {
      ...init,
      headers: {
        Authorization: `Bearer ${managerToken}`,
        'X-Opskeeper-Version': 'v1',
        ...(init?.body ? { 'Content-Type': 'application/json' } : {}),
        ...init?.headers,
      },
      cache: 'no-store',
      signal: AbortSignal.timeout(requestTimeoutMs),
    });
  } catch (error) {
    if (error instanceof DemoManagerError) throw error;
    if (error instanceof Error && error.name === 'TimeoutError') {
      throw new DemoManagerError('manager_timeout', 'Manager request timed out', 504);
    }
    throw new DemoManagerError('manager_unreachable', 'Manager is unreachable', 502);
  }

  let payload: unknown;
  try {
    payload = await response.json();
  } catch {
    throw new DemoManagerError(
      'invalid_manager_response',
      'Manager returned invalid JSON',
      502,
    );
  }

  if (!response.ok) {
    const errorPayload = payload as ManagerErrorEnvelope;
    throw new DemoManagerError(
      errorPayload.error_code ?? errorPayload.code ?? 'manager_error',
      errorPayload.message ?? 'Manager request failed',
      response.status,
    );
  }

  const envelope = payload as ManagerEnvelope<T>;
  if (envelope?.data === undefined || envelope?.data === null) {
    throw new DemoManagerError(
      'invalid_manager_response',
      'Manager response is missing data',
      502,
    );
  }
  return envelope.data;
}

export function startFinalDemoScenario(idempotencyKey: string) {
  const input: StartScenarioInput = {
    idempotency_key: idempotencyKey,
    scenario_id: FINAL_DEMO_CONFIG.scenarioId,
    target: FINAL_DEMO_CONFIG.target,
    target_fingerprint: FINAL_DEMO_CONFIG.targetFingerprint,
    alert_fingerprint: FINAL_DEMO_CONFIG.alertFingerprint,
    duration_seconds: FINAL_DEMO_CONFIG.durationSeconds,
  };

  return requestManager<ScenarioStatus>(
    `/api/v1/demo/scenarios/${encodeURIComponent(FINAL_DEMO_CONFIG.scenarioId)}/start`,
    { method: 'POST', body: JSON.stringify(input) },
  );
}

export function getFinalDemoScenario(idempotencyKey: string) {
  return requestManager<ScenarioStatus>(
    `/api/v1/demo/scenarios/${encodeURIComponent(idempotencyKey)}`,
  );
}

export function getBusinessSnapshot(
  idempotencyKey: string,
  section: BusinessSection,
) {
  return requestManager<BusinessSnapshot>(
    `/api/v1/demo/scenarios/${encodeURIComponent(idempotencyKey)}/business/${section}`,
  );
}
