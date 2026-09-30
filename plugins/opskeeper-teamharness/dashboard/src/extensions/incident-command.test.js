import assert from 'node:assert/strict';
import * as React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import test from 'node:test';
import { createServer } from 'vite';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { readFileSync } from 'node:fs';

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
  assert.equal(partial.view.stage, undefined);
  assert.equal(partial.view.stageStatus, 'unknown');
  assert.equal(partial.view.nextAction.kind, 'inspect-evidence');
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

test('partial incident-command reads never fabricate state or timeline data', async () => {
  const { fetchIncidentCommand } = await loadModule(
    '/src/extensions/incident-command/IncidentCommandRoute.jsx',
  );
  const timeline = {
    chain: { current_phase: 'recovered' },
    phases: [{ phase: 'recovered', status: 'running' }],
    events: [{ phase: 'recovered', event_type: 'phase_entered', created_at: '2026-10-01T12:00:00Z' }],
  };
  const state = {
    incident_id: 'inc-partial',
    current_phase: 'approved',
    updated_at: '2026-10-01T12:00:00Z',
    server_now: '2026-10-01T12:00:30Z',
  };
  const api = (stateError, timelineError) => ({
    getIncidentLoopState: async () => {
      if (stateError) throw new Error('state unavailable');
      return state;
    },
    getIncidentLoopTimeline: async () => {
      if (timelineError) throw new Error('timeline unavailable');
      return timeline;
    },
  });

  const stateOnly = await fetchIncidentCommand(api(false, true), 'inc-partial');
  assert.deepEqual(stateOnly.state, state);
  assert.equal(stateOnly.timeline, null);
  assert.equal(stateOnly.timelineError.message, 'timeline unavailable');
  assert.equal(stateOnly.view.stage, undefined);
  assert.equal(stateOnly.view.stageStatus, 'unknown');
  assert.ok(stateOnly.view.stageTimeline.every((stage) => stage.status === 'unknown'));

  const timelineOnly = await fetchIncidentCommand(api(true, false), 'inc-partial');
  assert.equal(timelineOnly.state, null);
  assert.equal(timelineOnly.stateError.message, 'state unavailable');
  assert.deepEqual(timelineOnly.timeline, timeline);
  assert.equal(timelineOnly.view.stage, 'recovered');
  assert.equal(timelineOnly.view.stageStatus, 'running');
});

test('freshness requires an explicit or state-provided server clock', async () => {
  const { fetchIncidentCommand } = await loadModule(
    '/src/extensions/incident-command/IncidentCommandRoute.jsx',
  );
  const api = (serverNow) => ({
    getIncidentLoopState: async () => ({
      incident_id: 'inc-clock',
      current_phase: 'approved',
      updated_at: '2000-01-01T00:00:00Z',
      ...(serverNow ? { server_now: serverNow } : {}),
    }),
    getIncidentLoopTimeline: async () => ({}),
  });

  const unknown = await fetchIncidentCommand(api(), 'inc-clock');
  assert.equal(unknown.view.freshness, 'unknown');
  assert.equal(unknown.view.serverNow, undefined);

  const fromState = await fetchIncidentCommand(api('2026-10-01T12:00:30Z'), 'inc-clock');
  assert.equal(fromState.view.freshness, 'stale');
  assert.equal(fromState.view.serverNow, '2026-10-01T12:00:30Z');

  const explicit = await fetchIncidentCommand(
    api('2026-10-01T00:00:30Z'),
    'inc-clock',
    '2000-01-01T00:00:30Z',
  );
  assert.equal(explicit.view.freshness, 'fresh');
  assert.equal(explicit.view.serverNow, '2000-01-01T00:00:30Z');
});

test('changing the selected incident clears the previous command projection', async () => {
  const { selectIncidentCommandIncident } = await loadModule(
    '/src/extensions/incident-command/IncidentCommandRoute.jsx',
  );
  const updates = [];
  selectIncidentCommandIncident(
    { target: { value: 'inc-b' } },
    {
      setSelectedIncidentId: (value) => updates.push(['incidentId', value]),
      setCommand: (value) => updates.push(['command', value]),
    },
  );

  assert.deepEqual(updates, [
    ['incidentId', 'inc-b'],
    ['command', null],
  ]);
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
  assert.match(unknown, /--ops-status-unknown/);

  const fresh = renderToStaticMarkup(React.createElement(CommandBar, {
    view: fromManagerLoop({
      state: { updated_at: '2026-10-01T12:00:00Z' },
      serverNow: '2026-10-01T12:00:30Z',
    }),
  }));
  assert.match(fresh, /数据新鲜/);
  assert.match(fresh, /--ops-status-waiting/);
  assert.doesNotMatch(fresh, /--ops-status-fresh|--ops-status-undefined/);
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

test('unified route statically integrates incident command and diagnostics', async () => {
  const { default: OpskeeperUnifiedRoute } = await loadModule('/src/extensions/unified-route.jsx');
  const unifiedSource = readFileSync(
    resolve(dashboardRoot, 'src/extensions/unified-route.jsx'),
    'utf8',
  );
  assert.match(unifiedSource, /import \{ opskeeperApi \} from '\.\/api\.js';/);
  assert.match(unifiedSource, /<IncidentCommandRoute\s+api=\{opskeeperApi\}/);
  assert.match(unifiedSource, /\{diagnosticsView === 'diagnostics' && <OpskeeperRoute api=\{api\} \/>}/);
  assert.match(unifiedSource, /\{diagnosticsView === 'integration' && <OpskeeperIntegrationRoute api=\{api\} \/>}/);
  assert.match(unifiedSource, /\{diagnosticsView === 'plugins' && <OpskeeperInstallView api=\{api\} \/>}/);
  assert.match(unifiedSource, /\{tab === 'evidence-approval' && <OpskeeperRoute api=\{api\} \/>}/);
  assert.match(unifiedSource, /\{tab === 'archive-replay' && <OpskeeperArchiveRoute api=\{api\} \/>}/);
  assert.match(unifiedSource, /\{tab === 'system-status' && <OpskeeperRuntimeRoute api=\{api\} \/>}/);
  const packageJson = JSON.parse(readFileSync(resolve(dashboardRoot, 'package.json'), 'utf8'));
  assert.match(packageJson.scripts.test, /src\/extensions\/incident-command\.test\.js/);

  const hostApi = { getDashboardRegistration: () => {} };
  assert.equal(hostApi.listIncidents, undefined);
  const markup = renderToStaticMarkup(React.createElement(OpskeeperUnifiedRoute, {
    api: hostApi,
    initialTab: 'incident-command',
  }));
  assert.match(markup, /aria-label="事故指挥与诊断"/);
  assert.match(markup, /aria-label="事故指挥"/);
  assert.match(markup, /事故诊断工具/);
  assert.match(markup, /诊断报告/);
  assert.match(markup, /链路自检/);
  assert.match(markup, /插件管理/);
});

test('stage timeline renders authoritative stage distinctions and source references', async () => {
  const { default: StageTimeline } = await loadModule('/src/extensions/incident-command/StageTimeline.jsx');
  const view = fromManagerLoop({
    incidentId: 'inc-stage',
    state: { incident_id: 'inc-stage', current_phase: 'approved' },
    timeline: {
      phases: [
        {
          phase: 'detected',
          status: 'success',
          worker_role: 'opskeeper-detector',
          started_at: '2026-10-01T11:58:00Z',
          ended_at: '2026-10-01T11:59:30Z',
          contract_summary: '确认业务告警',
          evidence_ref: 'archive:event-01',
        },
        { phase: 'correlated', status: 'success', worker_role: 'opskeeper-correlator', evidence_ref: 'archive:event-02' },
        { phase: 'investigated', status: 'success', worker_role: 'opskeeper-investigator', evidence_ref: 'archive:event-03' },
        { phase: 'critiqued', status: 'success', worker_role: 'opskeeper-critic', evidence_ref: 'archive:event-04' },
        { phase: 'approved', status: 'paused', worker_role: 'opskeeper-worker', evidence_ref: 'archive:event-05' },
      ],
      events: [
        { id: 'evt-detected', phase: 'detected', event_type: 'phase_entered', created_at: '2026-10-01T11:58:00Z', task_id: 'task-detected' },
        { id: 'evt-detected-done', phase: 'detected', event_type: 'phase_contract_written', created_at: '2026-10-01T11:59:30Z', task_id: 'task-detected' },
        { id: 'evt-approved', phase: 'approved', event_type: 'phase_entered', created_at: '2026-10-01T12:00:00Z', task_id: 'task-approval' },
        { id: 'evt-approved-pause', phase: 'approved', event_type: 'phase_paused', created_at: '2026-10-01T12:00:01Z', task_id: 'task-approval' },
      ],
    },
  });

  const markup = renderToStaticMarkup(React.createElement(StageTimeline, {
    stages: view.stageTimeline,
    onOpenEvidence: () => {},
  }));

  assert.match(markup, /告警确认/);
  assert.match(markup, /关联聚类/);
  assert.match(markup, /深度诊断/);
  assert.match(markup, /自批评审/);
  assert.match(markup, /人工审批/);
  assert.match(markup, /修复验证/);
  assert.match(markup, /复盘归档/);
  assert.match(markup, /aria-current="step"/);
  assert.match(markup, /data-stage="detected" data-status="completed"/);
  assert.match(markup, /data-stage="approved" data-status="blocked"/);
  assert.match(markup, /等待精准人工审批/);
  assert.match(markup, /等待人工/);
  assert.match(markup, /owner="Human"/);
  assert.match(markup, /90秒/);
  assert.match(markup, /确认业务告警/);
  assert.match(markup, /archive:event-01/);
  assert.match(markup, /事件 evt-detected-done/);
  assert.match(markup, /任务 task-detected/);
  assert.match(markup, /data-stage="recovered" data-status="future"/);
  assert.match(markup, /未开始/);
  assert.match(markup, /查看证据/);
  assert.match(markup, /opskeeper-incident-stage-grid/);
});

test('evidence projection preserves five decisions, source IDs, and legacy semantics', async () => {
  const { projectIncidentEvidence } = await loadModule('/src/extensions/archive.js');
  const archive = {
    incident_id: 'inc-evidence',
    closed: true,
    timeline: [
      { id: 'event-alert', event_type: 'alert_received', evidence_ref: 'evidence/alert.json' },
      { id: 'event-cause', event_type: 'root_cause', evidence_ref: 'evidence/cause.json', actor: 'investigator' },
      { id: 'event-approved', event_type: 'approved', evidence_ref: 'evidence/approval.json', actor: 'reviewer' },
      { id: 'event-action', event_type: 'action', evidence_ref: 'evidence/execution.json', actor: 'repairer' },
      { id: 'event-recovery', event_type: 'recovery', evidence_ref: 'evidence/verification.json', actor: 'verifier' },
    ],
    repair_previews: [{
      id: 'run-legacy',
      run_id: 'run-legacy',
      candidates: [
        { id: 'candidate-baseline', candidate_id: 'baseline', decision: 'PASS' },
        { id: 'candidate-a', candidate_id: 'candidate-a', decision: 'PASS', name: '扩容连接池' },
        { id: 'candidate-b', candidate_id: 'candidate-b', decision: 'REJECTED_BY_PREVIEW', rejection_reason: '写入影响超界' },
      ],
    }],
    postmortem_refs: [{ id: 'postmortem-1', root_cause: '连接池耗尽' }],
  };
  const preview = {
    run_id: 'run-1',
    seed_fingerprint: 'sha256:seed-v1',
    workload_fingerprint: 'sha256:workload-v1',
    controlled_load: true,
    isolation_boundary: 'preview-pg',
    target_fingerprint: 'sha256:target-v1',
    status: 'completed',
    passing: { id: 'candidate-a', candidate_id: 'candidate-a', name: '扩容连接池' },
  };

  const groups = projectIncidentEvidence({ archive, preview });
  assert.deepEqual(groups.map((group) => group.id), ['incident', 'cause', 'repair', 'safety', 'verification']);
  assert.equal(groups.find((group) => group.id === 'incident').facts.find((fact) => fact.id === 'alert').sourceIds[0], 'event-alert');
  assert.equal(groups.find((group) => group.id === 'cause').facts.find((fact) => fact.id === 'rootCause').value, '连接池耗尽');
  const repair = groups.find((group) => group.id === 'repair');
  assert.equal(repair.facts.find((fact) => fact.id === 'comparison').value.includes('candidate-a'), true);
  assert.equal(repair.facts.find((fact) => fact.id === 'eligibility').value, '预览已完成');
  const safety = groups.find((group) => group.id === 'safety');
  assert.equal(safety.facts.find((fact) => fact.id === 'targetFingerprint').value, 'sha256:target-v1');
  assert.equal(safety.facts.find((fact) => fact.id === 'workloadFingerprint').value, 'sha256:workload-v1');
  assert.equal(safety.facts.find((fact) => fact.id === 'approval').sourceIds[0], 'event-approved');
  assert.equal(groups.find((group) => group.id === 'verification').facts.find((fact) => fact.id === 'result').sourceIds[0], 'event-recovery');

  const legacy = projectIncidentEvidence({
    archive: { ...archive, repair_previews: [], legacy_preview_not_applicable: true },
    preview: null,
  });
  assert.equal(legacy.find((group) => group.id === 'repair').completeness, 'legacy_not_applicable');
});

test('evidence drawer is a semantic modal with nested raw disclosures and focus cycling', async () => {
  const drawerModule = await loadModule('/src/extensions/incident-command/EvidenceDrawer.jsx');
  const {
    default: EvidenceDrawer,
    handleEvidenceDrawerKeyDown,
    initializeEvidenceDrawerFocus,
  } = drawerModule;
  const groups = [{
    id: 'repair',
    title: '为什么选择这个修复',
    completeness: 'complete',
    facts: [{ id: 'comparison', label: 'A/B 对比', value: 'candidate-a 优于 baseline' }],
    rawPayload: { candidates: ['baseline', 'candidate-a'] },
  }];
  const markup = renderToStaticMarkup(React.createElement(EvidenceDrawer, {
    open: true,
    groups,
    onClose: () => {},
  }));

  assert.match(markup, /role="dialog"/);
  assert.match(markup, /aria-modal="true"/);
  assert.match(markup, /aria-labelledby="opskeeper-evidence-title"/);
  assert.match(markup, /<summary[^>]*>为什么选择这个修复<span[^>]*>完整<\/span><\/summary>/);
  assert.match(markup, /<summary[^>]*>原始只读载荷<\/summary>/);
  assert.match(markup, /关闭证据抽屉/);

  const focused = [];
  const targets = [
    { disabled: false, focus: () => focused.push('first') },
    { disabled: false, focus: () => focused.push('close') },
    { disabled: false, focus: () => focused.push('raw') },
  ];
  const prevented = [];
  const closeCalls = [];
  const container = { querySelectorAll: () => targets };
  handleEvidenceDrawerKeyDown({
    key: 'Tab',
    shiftKey: true,
    preventDefault: () => prevented.push('wrap'),
  }, { container, activeElement: targets[0], onClose: () => closeCalls.push('close') });
  handleEvidenceDrawerKeyDown({
    key: 'Escape',
    preventDefault: () => prevented.push('escape'),
  }, { container, activeElement: targets[1], onClose: () => closeCalls.push('close') });

  assert.deepEqual(focused, ['raw']);
  assert.deepEqual(prevented, ['wrap', 'escape']);
  assert.deepEqual(closeCalls, ['close']);

  const originalDocument = globalThis.document;
  const originalActiveElement = {
    focused: [],
    focus() { this.focused.push('original'); },
  };
  const closeButton = { focused: [], focus() { this.focused.push('close'); } };
  globalThis.document = { activeElement: originalActiveElement };
  const restoreFocus = initializeEvidenceDrawerFocus(closeButton);
  assert.deepEqual(closeButton.focused, ['close']);
  globalThis.document.activeElement = closeButton;
  restoreFocus();
  assert.deepEqual(originalActiveElement.focused, ['original']);
  globalThis.document = originalDocument;
});

test('approval checklist presents facts and warns on missing precise context without mutation', async () => {
  const { default: ApprovalChecklist } = await loadModule('/src/extensions/incident-command/ApprovalChecklist.jsx');
  const complete = {
    incidentId: 'inc-approval',
    candidateId: 'candidate-a',
    executionId: 'event-action',
    targetFingerprint: 'sha256:target-v1',
    workloadFingerprint: 'sha256:workload-v1',
    impactScope: 'pg:pool-fixture',
    parameters: { command: 'resize_pool', pool_manifest_id: 'pool-v1' },
    expiresAt: '2026-10-01T12:30:00Z',
    serverNow: '2026-10-01T12:00:00Z',
    rollbackPlan: '保留旧连接池配置并支持一键回滚',
    previewEligibility: '预览已完成',
    verificationCriteria: '业务探针通过且延迟恢复基线',
    approvalStatus: 'awaiting_human',
    instruction: 'approve incident=inc-approval candidate=candidate-a',
    channel: 'Manager 人工审批中心',
  };
  const markup = renderToStaticMarkup(React.createElement(ApprovalChecklist, { facts: complete }));
  for (const value of Object.values(complete)) {
    assert.match(markup, new RegExp(String(typeof value === 'object' ? 'resize_pool' : value).replace(/[.*+?^${}()|[\]\\]/g, '\\$&')));
  }
  assert.match(markup, /复制精确审批指令/);
  assert.match(markup, /本清单仅呈现事实，不授予执行权限/);
  assert.doesNotMatch(markup, /批准|拒绝|提交|执行修复/);

  const missing = renderToStaticMarkup(React.createElement(ApprovalChecklist, {
    facts: { approvalStatus: 'awaiting_human', channel: 'Manager 人工审批中心' },
  }));
  assert.match(missing, /缺少 incident 精确上下文/);
  assert.match(missing, /缺少 candidate 精确上下文/);
  assert.match(missing, /无法生成精确审批指令/);
  assert.doesNotMatch(missing, /已授权|已批准/);

  const componentSource = readFileSync(
    resolve(dashboardRoot, 'src/extensions/incident-command/ApprovalChecklist.jsx'),
    'utf8',
  );
  assert.doesNotMatch(componentSource, /fetch\(|method:\s*['"](POST|PUT|PATCH|DELETE)/);
});

test('incident command reads evidence readback and integrates presentation-only surfaces', async () => {
  const { fetchIncidentCommand } = await loadModule('/src/extensions/incident-command/IncidentCommandRoute.jsx');
  const calls = [];
  const api = {
    async getIncidentLoopState(id) { calls.push(['state', id]); return { incident_id: id, current_phase: 'approved' }; },
    async getIncidentLoopTimeline(id) { calls.push(['timeline', id]); return { phases: [], events: [] }; },
    async getIncidentArchive(id) { calls.push(['archive', id]); return { incident_id: id, timeline: [] }; },
    async getIncidentRepairPreviewSummary(id) { calls.push(['preview', id]); return { incident_id: id, run_id: 'run-1' }; },
  };
  const result = await fetchIncidentCommand(api, 'inc-readback');
  assert.deepEqual(calls, [
    ['state', 'inc-readback'],
    ['timeline', 'inc-readback'],
    ['archive', 'inc-readback'],
    ['preview', 'inc-readback'],
  ]);
  assert.equal(result.evidenceGroups.length, 5);
  assert.equal(result.approvalFacts.incidentId, 'inc-readback');

  const apiSource = readFileSync(resolve(dashboardRoot, 'src/extensions/api.js'), 'utf8');
  assert.doesNotMatch(apiSource, /(?:approve|reject|submit)Incident|approval.*(?:POST|PUT|PATCH|DELETE)/i);
  const routeSource = readFileSync(
    resolve(dashboardRoot, 'src/extensions/incident-command/IncidentCommandRoute.jsx'),
    'utf8',
  );
  assert.doesNotMatch(routeSource, /(?:approve|reject|submit)Incident|method:\s*['"](POST|PUT|PATCH|DELETE)/i);
});

test('evidence read failures remain independent and never fabricate approval safety facts', async () => {
  const { fetchIncidentCommand } = await loadModule('/src/extensions/incident-command/IncidentCommandRoute.jsx');
  const result = await fetchIncidentCommand({
    getIncidentLoopState: async () => ({
      incident_id: 'inc-evidence-error',
      current_phase: 'approved',
      updated_at: '2026-10-01T12:00:00Z',
      server_now: '2026-10-01T12:00:30Z',
    }),
    getIncidentLoopTimeline: async () => ({
      phases: [{ phase: 'approved', status: 'paused' }],
      events: [{ id: 'pause-1', phase: 'approved', event_type: 'phase_paused' }],
    }),
    getIncidentArchive: async () => { throw new Error('archive unavailable'); },
    getIncidentRepairPreviewSummary: async () => { throw new Error('preview unavailable'); },
  }, 'inc-evidence-error');

  assert.equal(result.view.stage, 'approved');
  assert.equal(result.view.stageStatus, 'blocked');
  assert.equal(result.archiveError.message, 'archive unavailable');
  assert.equal(result.previewError.message, 'preview unavailable');
  assert.equal(result.evidenceGroups.length, 5);
  assert.ok(result.evidenceGroups.every((group) => group.completeness === 'missing'));
  assert.equal(result.approvalFacts.targetFingerprint, '');
  assert.equal(result.approvalFacts.workloadFingerprint, '');
  assert.equal(result.approvalFacts.instruction, '');

  const routeSource = readFileSync(
    resolve(dashboardRoot, 'src/extensions/incident-command/IncidentCommandRoute.jsx'),
    'utf8',
  );
  assert.match(routeSource, /正在读取权威事故状态/);
  assert.match(routeSource, /暂无可选事故/);
  assert.match(routeSource, /重试只读刷新/);
});
