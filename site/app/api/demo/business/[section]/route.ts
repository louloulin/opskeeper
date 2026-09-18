import { NextRequest, NextResponse } from 'next/server';
import { DemoManagerError, getBusinessSnapshot } from '@/lib/demo-manager';
import { BUSINESS_SECTIONS, type BusinessSection } from '@/lib/demo-types';

export const dynamic = 'force-dynamic';
export const runtime = 'nodejs';

const scenarioCookieName = 'opskeeper_demo_scenario';

function isBusinessSection(value: string): value is BusinessSection {
  return (BUSINESS_SECTIONS as readonly string[]).includes(value);
}

export async function GET(
  request: NextRequest,
  { params }: { params: { section: string } },
) {
  if (!isBusinessSection(params.section)) {
    return NextResponse.json(
      { error_code: 'invalid_section', message: 'Unknown business section' },
      { status: 400, headers: { 'Cache-Control': 'no-store' } },
    );
  }

  const key = request.cookies.get(scenarioCookieName)?.value;
  if (!key) {
    return NextResponse.json(
      { error_code: 'demo_scenario_not_started', message: 'No current demo scenario' },
      { status: 503, headers: { 'Cache-Control': 'no-store' } },
    );
  }

  try {
    const snapshot = await getBusinessSnapshot(key, params.section);
    return NextResponse.json(snapshot, {
      headers: { 'Cache-Control': 'no-store' },
    });
  } catch (error) {
    if (
      error instanceof DemoManagerError &&
      (error.status === 503 || error.code === 'manager_timeout')
    ) {
      return NextResponse.json(
        { error_code: 'pool_exhausted', message: 'Business query unavailable' },
        { status: 503, headers: { 'Cache-Control': 'no-store' } },
      );
    }
    if (error instanceof DemoManagerError) {
      return NextResponse.json(
        { error_code: error.code, message: error.message },
        { status: error.status, headers: { 'Cache-Control': 'no-store' } },
      );
    }
    return NextResponse.json(
      { error_code: 'business_proxy_failed', message: 'Business proxy failed' },
      { status: 500, headers: { 'Cache-Control': 'no-store' } },
    );
  }
}
