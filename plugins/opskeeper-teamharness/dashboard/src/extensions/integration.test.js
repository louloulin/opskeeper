import assert from 'node:assert/strict';
import test from 'node:test';

import {
  buildIntegrationPreflight,
  joinedRoomsFromSync,
  roomMatchesTarget,
} from './integration.js';

function fulfilled(value) {
  return { status: 'fulfilled', value };
}

function rejected(message) {
  return { status: 'rejected', reason: new Error(message) };
}

const matrixSync = {
  rooms: {
    join: {
      '!room-a:example.com': {
        state: {
          events: [
            { type: 'm.room.canonical_alias', content: { alias: '#ops:example.com' } },
          ],
        },
      },
      '!room-b:example.com': {},
    },
  },
};

const healthyResults = {
  session: fulfilled({ authenticated: true, username: 'demo' }),
  matrixSync: fulfilled(matrixSync),
  agentteamsHealth: fulfilled('ok'),
  opskeeperHealth: fulfilled({ status: 'ok' }),
  pluginHealth: fulfilled({
    synced: true,
    diff: [],
    worker: { version: '1.0.66', loaded: true, enabled: true },
  }),
  manifest: fulfilled({
    id: 'opskeeper-teamharness',
    version: '1.0.66',
    entry: { dashboard: 'dist/main-1.0.66.js' },
  }),
};

test('extracts and matches Matrix rooms by id or canonical alias', () => {
  assert.deepEqual(joinedRoomsFromSync(matrixSync), ['!room-a:example.com', '!room-b:example.com']);
  assert.equal(roomMatchesTarget(matrixSync, '!room-a:example.com'), true);
  assert.equal(roomMatchesTarget(matrixSync, '#ops:example.com'), true);
  assert.equal(roomMatchesTarget(matrixSync, '#missing:example.com'), false);
});

test('builds a passing room-to-plugin integration report', () => {
  const report = buildIntegrationPreflight(healthyResults, {
    targetRoomId: '#ops:example.com',
    expectedPluginVersion: '1.0.66',
  });

  assert.equal(report.status, 'pass');
  assert.equal(report.joinedRoomCount, 2);
  assert.deepEqual(report.checks.map((item) => item.status), [
    'pass', 'pass', 'pass', 'pass', 'pass', 'pass',
  ]);
});

test('warns when no target room is provided', () => {
  const report = buildIntegrationPreflight(healthyResults, {
    expectedPluginVersion: '1.0.66',
  });

  assert.equal(report.status, 'warn');
  assert.equal(report.checks.find((item) => item.name === '目标房间').status, 'warn');
});

test('reports Matrix and worker failures as blocking checks', () => {
  const report = buildIntegrationPreflight({
    ...healthyResults,
    matrixSync: rejected('Matrix 登录态不可用'),
    pluginHealth: fulfilled({ synced: false, diff: ['absent_on_worker'], worker: null }),
  }, {
    targetRoomId: '#ops:example.com',
    expectedPluginVersion: '1.0.66',
  });

  assert.equal(report.status, 'fail');
  assert.equal(report.checks.find((item) => item.name === '房间会话与 Matrix 同步').status, 'fail');
  assert.equal(report.checks.find((item) => item.name === 'TeamHarness Worker 插件').status, 'fail');
});

test('warns when the target room is owned by an OpsKeeper Matrix account', () => {
  const report = buildIntegrationPreflight(healthyResults, {
    targetRoomId: '#benyue-lumos-ops:matrix-local.agentteams.io',
    expectedPluginVersion: '1.0.66',
  });

  assert.equal(report.status, 'warn');
  const roomCheck = report.checks.find((item) => item.name === '目标房间');
  assert.equal(roomCheck.status, 'warn');
  assert.match(roomCheck.detail, /OpsKeeper Manager/);
});
