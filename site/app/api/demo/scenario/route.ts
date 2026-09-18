import { NextRequest, NextResponse } from 'next/server';
import {
  DemoManagerError,
  startFinalDemoScenario,
  getFinalDemoScenario,
} from '@/lib/demo-manager';
import type { ScenarioStatus } from '@/lib/demo-types';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

const scenarioCookieName = 'opskeeper_demo_scenario';

function idempotencyKey() {
  const now = new Date();
  const year = now.getUTCFullYear();
  const month = `${now.getUTCMonth() + 1}`.padStart(2, '0');
  const day = `${now.getUTCDate()}`.padStart(2, '0');
  const hour = `${now.getUTCHours()}`.padStart(2, '0');
  const minute = `${now.getUTCMinutes()}`.padStart(2, '0');
  return `final-demo-${year}${month}${day}-${hour}${minute}`;
}

function errorResponse(error: unknown) {
  if (error instanceof DemoManagerError) {
    return NextResponse.json(
      { error_code: error.code, message: error.message },
      { status: error.status, headers: { 'Cache-Control': 'no-store' } },
    );
  }
  return NextResponse.json(
    { error_code: 'scenario_proxy_failed', message: 'Scenario proxy failed' },
    { status: 500, headers: { 'Cache-Control': 'no-store' } },
  );
}

export async function GET(request: NextRequest) {
  const key = request.cookies.get(scenarioCookieName)?.value;
  if (!key) {
    return NextResponse.json(
      { error_code: 'demo_scenario_not_started', message: 'No current demo scenario' },
      { status: 404, headers: { 'Cache-Control': 'no-store' } },
    );
  }

  try {
    return NextResponse.json(await getFinalDemoScenario(key), {
      headers: { 'Cache-Control': 'no-store' },
    });
  } catch (error) {
    return errorResponse(error);
  }
}

export async function POST() {
  const key = idempotencyKey();
  try {
    const status = await startFinalDemoScenario(key);
    const response = NextResponse.json(status, {
      status: 201,
      headers: { 'Cache-Control': 'no-store' },
    });
    response.cookies.set({
      name: scenarioCookieName,
      value: key,
      httpOnly: true,
      sameSite: 'lax',
      secure: process.env.NODE_ENV === 'production',
      path: '/',
      maxAge: 3_600,
    });
    return response;
  } catch (error) {
    return errorResponse(error);
  }
}
