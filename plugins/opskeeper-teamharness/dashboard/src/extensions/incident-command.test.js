import assert from 'node:assert/strict';
import * as React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import test from 'node:test';
import { createServer } from 'vite';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

import { opskeeperApi, xhrTransport } from './api.js';
import { fromManagerLoop, selectNextAction } from '../../../../../shared/incident-command/index.js';

const dashboardRoot = resolve(fileURLToPath(new URL('.', import.meta.url)), '../..');
const vite = await createServer({
  configFile: false,
  root: dashboardRoot,
  appType: 'custom',
  logLevel: 'error',
  resolve: {
    alias: {
      '@opskeeper/incident-command': resolve(dashboardRoot, '../../../shared/incident-command/index.js'),
    },
  },
});

test.after(async () => {
  await vite.close();
});

async function loadModule(path) {
  return vite.ssrLoadModule(path);
}

function useJsonXhr(responses) {
  const opened = [];
  xhrTransport.createRequest = () => {
    const request = {
      status: 200,
      responseText: '',
      headers: {},
      open(method, url) {
        opened.push({ method, url });
      },
      setRequestHeader(name, value) {
        request.headers[name] = value;
      },
      getResponseHeader(name) {
        return request.headers[name] ?? null;
      },
      send() {
        queueMicrotask(() => {
          const match = opened.at(-1);
          const response = responses[match.url] ?? {};
          request.status = response.status ?? 200;
          request.responseText = JSON.stringify(response.body ?? {});
          request.onload();
        });
      },
    };
    return request;
  };
  return opened;
}

test('incident loop read APIs use encoded GET-only paths', async () => {
  const opened = useJsonXhr({
    '/api/opskeeper/loops/inc%20A/state': { incident_id: 'inc A' },
    '/api/opskeeper/loops/team%2Floop/timeline': { phases: [] },
  });

  await opskeeperApi.getIncidentLoopState('inc A');
  await opskeeperApi.getIncidentLoopTimeline('team/loop');

  assert.deepEqual(opened.map(({ method, url }) => ({ method, url })), [
    { method: 'GET', url: '/api/opskeeper/loops/inc%20A/state' },
    { method: 'GET', url: '/api/opskeeper/loops/team%2Floop/timeline' },
  ]);
});

test('command polling follows the selected incident and keeps read errors independent', async () => {
  const { fetchIncidentCommand, startIncidentCommandPolling } = await loadModule(
    '/src/extensions/incident-command/IncidentCommandRoute.jsx',
  );
  const calls = [];
  const api = (stateError, timelineError) => ({
    async getIncidentLoopState(id) {
      calls.push(['state', id]);
      if (stateError) throw new Error('state unavailable');
      return { incident_id: id, current_phase: 'approved', updated_at: '2026-10-01T12:00:30Z' };
    },
    async getIncidentLoopTimeline(id) {
      calls.push(['timeline', id]);
      if (timelineError) throw new Error('timeline unavailable');
      return {
        phases: [{ phase: 'approved', status: 'running' }],
        events: [{ id: 7, phase: 'approved', event_type: 'phase_paused', created_at: '2026-10-01T12:00:00Z' }],
      };
    },
  });

  const partial = await fetchIncidentCommand(api(false, true), 'inc-a', '2026-10-01T12:00:30Z');
  assert.equal(partial.view.incidentId, 'inc-a');
  assert.equal(partial.view.stage, 'approved');
  assert.equal(partial.view.nextAction.kind, 'wait');
  assert.equal(partial.stateError, null);
  assert.equal(partial.timelineError.message, 'timeline unavailable');

  const reverse = await fetchIncidentCommand(api(true, false), 'inc-b', '2026-10-01T12:00:30Z');
  assert.equal(reverse.view.incidentId, 'inc-b');
  assert.equal(reverse.stateError.message, 'state unavailable');
  assert.equal(reverse.timelineError, null);

  const observed = [];
  const pollingCallStart = calls.length;
  const stop = startIncidentCommandPolling({
    api: api(false, false),
    incidentId: 'inc-a',
    intervalMs: 1,
    onData: (result) => observed.push(result),
  });
  await new Promise((resolveTest) => {
    const timer = setInterval(() => {
      if (observed.length >= 2) {
        clearInterval(timer);
        resolveTest();
      }
    }, 1);
  });
  stop();
  const beforeStop = calls.length;
  await new Promise((resolveTest) => setTimeout(resolveTest, 5));
  assert.equal(calls.length, beforeStop);
  assert(calls.slice(pollingCallStart).every(([, id]) => id === 'inc-a'));
});

test('command bar renders textual stale and unknown freshness with semantic time', async () => {
  const { default: CommandBar } = await loadModule('/src/extensions/incident-command/CommandBar.jsx');
  const view = fromManagerLoop({
    state: { incident_id: 'inc-stale', current_phase: 'approved', updated_at: '2026-10-01T11:00:00Z' },
    serverNow: '2026-10-01T12:00:30Z',
  });
  const stale = renderToStaticMarkup(React.createElement(CommandBar, { view }));
  assert.match(stale, /数据已过期/);
  assert.match(stale, /新鲜度有效期/);
  assert.match(stale, /<time[^>]*[dD]ate[tT]ime="2026-10-01T11:00:00Z"/);

  const unknown = renderToStaticMarkup(React.createElement(CommandBar, {
    view: fromManagerLoop({}),
  }));
  assert.match(unknown, /数据新鲜度未知/);
  assert.match(unknown, /当前阶段未知/);
});

test('next-action card renders the deterministic highest-priority action', async () => {
  const { default: NextActionCard } = await loadModule('/src/extensions/incident-command/NextActionCard.jsx');
  const action = selectNextAction({
    candidates: [
      { kind: 'wait', priority: 60, label: '等待权威流程推进' },
      { kind: 'approve', priority: 20, label: '提供精准人工审批', detail: '审批必须包含 incident 与 candidate 精确上下文' },
    ],
  });
  const markup = renderToStaticMarkup(React.createElement(NextActionCard, { action }));
  assert.match(markup, /提供精准人工审批/);
  assert.match(markup, /P20/);
  assert.doesNotMatch(markup, /等待权威流程推进/);
});

test('diagnostics controls and legacy report remain reachable below command summary', async () => {
  const { default: IncidentCommandRoute } = await loadModule(
    '/src/extensions/incident-command/IncidentCommandRoute.jsx',
  );
  const { default: DiagnosticsMenu } = await loadModule('/src/extensions/incident-command/DiagnosticsMenu.jsx');

  const menu = renderToStaticMarkup(React.createElement(DiagnosticsMenu, {
    view: 'diagnostics',
    onSelect: () => {},
  }));
  assert.match(menu, /诊断报告/);
  assert.match(menu, /链路自检/);
  assert.match(menu, /插件管理/);

  const route = renderToStaticMarkup(React.createElement(IncidentCommandRoute, {
    api: {},
    initialIncidentId: 'inc-a',
    onOpenDiagnostics: () => {},
  }));
  assert.match(route, /aria-label="事故指挥"/);
  assert.match(route, /查看证据与诊断/);
});
