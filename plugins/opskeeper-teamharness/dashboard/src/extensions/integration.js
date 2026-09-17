export function joinedRoomsFromSync(sync) {
  const rooms = sync?.rooms?.join;
  if (!rooms || typeof rooms !== 'object') return [];
  return Object.keys(rooms);
}

function roomAliasesFromSync(sync, roomId) {
  const events = sync?.rooms?.join?.[roomId]?.state?.events;
  if (!Array.isArray(events)) return [];
  return events
    .filter((event) => event?.type === 'm.room.canonical_alias')
    .map((event) => event?.content?.alias)
    .filter(Boolean);
}

export function roomMatchesTarget(sync, targetRoomId) {
  const normalizedTarget = String(targetRoomId || '').trim().toLowerCase();
  if (!normalizedTarget) return false;
  const rooms = joinedRoomsFromSync(sync);
  return rooms.some((roomId) => {
    if (roomId.toLowerCase() === normalizedTarget) return true;
    return roomAliasesFromSync(sync, roomId)
      .some((alias) => String(alias).toLowerCase() === normalizedTarget);
  });
}

function valueOf(result) {
  return result?.status === 'fulfilled' ? result.value : null;
}

function errorOf(result) {
  return result?.status === 'rejected' ? result.reason?.message || '读取失败' : null;
}

function check(status, name, detail) {
  return { status, name, detail };
}

function healthStatus(value) {
  const status = value?.status || value?.data?.status || value?.health?.status;
  return status ? String(status).toLowerCase() : 'ok';
}

export function buildIntegrationPreflight(results, { targetRoomId = '', expectedPluginVersion = '' } = {}) {
  const session = valueOf(results.session);
  const matrixSync = valueOf(results.matrixSync);
  const agentteamsHealth = valueOf(results.agentteamsHealth);
  const opskeeperHealth = valueOf(results.opskeeperHealth);
  const pluginHealth = valueOf(results.pluginHealth);
  const manifest = valueOf(results.manifest);
  const checks = [];

  if (session?.authenticated) {
    checks.push(check('pass', 'Dashboard 登录态', session.username ? `用户：${session.username}` : '已登录'));
  } else {
    checks.push(check('fail', 'Dashboard 登录态', errorOf(results.session) || '会话未认证，请重新登录'));
  }

  const matrixError = errorOf(results.matrixSync);
  if (matrixError) {
    checks.push(check('fail', '房间会话与 Matrix 同步', matrixError));
  } else {
    const rooms = joinedRoomsFromSync(matrixSync);
    const normalizedTarget = String(targetRoomId || '').trim();
    if (!normalizedTarget) {
      checks.push(check('warn', '目标房间', '未填写房间 ID / alias，仅确认 Matrix 同步可用'));
    } else if (roomMatchesTarget(matrixSync, normalizedTarget)) {
      checks.push(check('pass', '目标房间', `${normalizedTarget} 已加入，可接收协同消息`));
    } else {
      checks.push(check('fail', '目标房间', `${normalizedTarget} 未在当前 Matrix 登录态的 ${rooms.length} 个已加入房间中`));
    }
  }

  if (errorOf(results.agentteamsHealth)) {
    checks.push(check('fail', 'AgentTeams 控制面', errorOf(results.agentteamsHealth)));
  } else {
    checks.push(check('pass', 'AgentTeams 控制面', `healthz 可达：${String(agentteamsHealth ?? 'ok').slice(0, 120)}`));
  }

  if (errorOf(results.opskeeperHealth)) {
    checks.push(check('fail', 'OpsKeeper 后端', errorOf(results.opskeeperHealth)));
  } else if (['error', 'down', 'critical'].includes(healthStatus(opskeeperHealth))) {
    checks.push(check('fail', 'OpsKeeper 后端', `健康状态异常：${healthStatus(opskeeperHealth)}`));
  } else {
    checks.push(check('pass', 'OpsKeeper 后端', '健康接口可达'));
  }

  const worker = pluginHealth?.worker;
  if (errorOf(results.pluginHealth)) {
    checks.push(check('fail', 'TeamHarness Worker 插件', errorOf(results.pluginHealth)));
  } else if (!pluginHealth?.synced || Array.isArray(pluginHealth?.diff) && pluginHealth.diff.length > 0 || !worker?.loaded || !worker?.enabled) {
    checks.push(check('fail', 'TeamHarness Worker 插件', `同步状态：${pluginHealth?.synced ? 'synced' : 'not synced'}；worker loaded=${worker?.loaded}, enabled=${worker?.enabled}`));
  } else if (expectedPluginVersion && worker?.version !== expectedPluginVersion) {
    checks.push(check('fail', 'TeamHarness Worker 插件', `worker 版本 ${worker?.version || '未知'}，期望 ${expectedPluginVersion}`));
  } else {
    checks.push(check('pass', 'TeamHarness Worker 插件', `v${worker?.version || expectedPluginVersion} loaded/enabled/in_sync`));
  }

  if (errorOf(results.manifest)) {
    checks.push(check('fail', 'Dashboard 插件清单', errorOf(results.manifest)));
  } else if (manifest?.id !== 'opskeeper-teamharness' || (expectedPluginVersion && manifest?.version !== expectedPluginVersion)) {
    checks.push(check('fail', 'Dashboard 插件清单', `manifest=${manifest?.id || '未知'}@${manifest?.version || '未知'}，期望 opskeeper-teamharness@${expectedPluginVersion}`));
  } else {
    checks.push(check('pass', 'Dashboard 插件清单', `v${manifest.version} 入口 ${manifest?.entry?.dashboard || '未知'}`));
  }

  const hasFailure = checks.some((item) => item.status === 'fail');
  const hasWarning = checks.some((item) => item.status === 'warn');
  return {
    checkedAt: new Date().toISOString(),
    status: hasFailure ? 'fail' : hasWarning ? 'warn' : 'pass',
    checks,
    joinedRoomCount: matrixSync ? joinedRoomsFromSync(matrixSync).length : 0,
  };
}
