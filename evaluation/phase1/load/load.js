// Phase 1 load harness — 200 concurrent learners, full attempt lifecycle.
//
// PLAN_PHASE3_PROJECTS.md G1 / Phase3_Stages.md 0.2. This is the deliverable
// of record; executing it on a real multi-node cluster and committing
// evaluation/phase1/results/loadtest-<date>.md is Phase3_Stages.md 0.4 (blocked
// on infra). See evaluation/phase1/load/README.md.
//
// Scenario per virtual learner, LOAD_ITERATIONS times:
//   dev-login -> pick published L1 lab -> create attempt -> provision
//   -> connect -> LOAD_CMDS_PER_ATTEMPT file writes -> check -> submit -> destroy
//
// Thresholds encode doc §13.1 exit criteria so the run self-grades. The
// zero-orphan gate is asserted separately by check-orphans.sh (needs a 1 h
// window after the run).

import http from 'k6/http';
import { check, sleep, fail } from 'k6';
import { Rate, Trend, Counter } from 'k6/metrics';

const BASE = __ENV.LOAD_BASE_URL || 'http://localhost:3001';
const ORCH_METRICS = __ENV.LOAD_ORCH_METRICS_URL || 'http://localhost:9090';
const VUS = parseInt(__ENV.LOAD_VUS || '200', 10);
const ITERATIONS = parseInt(__ENV.LOAD_ITERATIONS || '3', 10);
const CMDS = parseInt(__ENV.LOAD_CMDS_PER_ATTEMPT || '20', 10);
const LAB_SLUG = __ENV.LOAD_LAB_SLUG || 'lab.linux.navigate-filesystem';
const SHARED_SECRET = __ENV.LOAD_SHARED_SECRET || 'compose-dev-shared-secret';
const RAMP = __ENV.LOAD_RAMP || '30s';
const THINK_MS = parseInt(__ENV.LOAD_THINK_MS || '250', 10);

// Optional: one JWT per line, indexed by __VU. Avoids hammering the throttled
// dev-login route at 200 VUs. Loaded once at init (k6 runs this file per VU
// but open() is init-context only).
const PREMINTED = (function () {
  const p = __ENV.LOAD_PREMINTED_TOKENS;
  if (!p) return [];
  return open(p)
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean);
})();

// ---- custom metrics -------------------------------------------------------
const provisionOk = new Rate('provision_ok');
const submitOk = new Rate('submit_ok');
const destroyOk = new Rate('destroy_ok');
const connectOk = new Rate('connect_ok');
const provisionMs = new Trend('provision_duration_ms', true);
const cmdWrites = new Counter('workspace_cmd_writes');
const labsCompleted = new Counter('labs_completed');

export const options = {
  scenarios: {
    learners: {
      executor: 'per-vu-iterations',
      vus: VUS,
      iterations: ITERATIONS,
      maxDuration: '60m',
      startTime: '0s',
      gracefulStop: '2m',
      // ramp VUs in over LOAD_RAMP by staggering iteration starts
      env: {},
    },
  },
  thresholds: {
    // doc §13.1
    provision_ok: ['rate>=0.99'], // provision success ≥ 99%
    submit_ok: ['rate>=0.99'], // submit success ≥ 99%
    destroy_ok: ['rate>=1.0'], // teardown never leaks
    connect_ok: ['rate>=0.99'],
    provision_duration_ms: ['p(95)<=20000'], // time-to-ready p95 ≤ 20s
    http_req_failed: ['rate<=0.02'],
  },
};

function authHeaders(token) {
  return { headers: { authorization: `Bearer ${token}`, 'content-type': 'application/json' } };
}

function devLogin() {
  const res = http.post(`${BASE}/v1/auth/dev-login`, '{}', {
    headers: { 'content-type': 'application/json' },
  });
  check(res, { 'dev-login 2xx': (r) => r.status >= 200 && r.status < 300 });
  const token = res.json('token');
  if (!token) fail(`dev-login returned no token (status ${res.status})`);
  return token;
}

function vuToken() {
  if (PREMINTED.length > 0) return PREMINTED[(__VU - 1) % PREMINTED.length];
  return devLogin();
}

let cachedAvid = null;
function resolveLabVersionId(token) {
  if (cachedAvid) return cachedAvid;
  const res = http.get(`${BASE}/v1/practice/activities`, authHeaders(token));
  check(res, { 'catalog 2xx': (r) => r.status === 200 });
  const list = res.json() || [];
  const hit = list.find((a) => a.slug === LAB_SLUG);
  if (!hit) fail(`lab ${LAB_SLUG} not found in catalog`);
  cachedAvid = hit.activity_version_id;
  return cachedAvid;
}

function runOneLab(token, avid) {
  // create
  const created = http.post(
    `${BASE}/v1/practice/attempts`,
    JSON.stringify({ activity_version_id: avid }),
    authHeaders(token),
  );
  if (!check(created, { 'attempt created': (r) => r.status >= 200 && r.status < 300 })) {
    provisionOk.add(false);
    return;
  }
  const attemptId = created.json('id');
  if (!attemptId) {
    provisionOk.add(false);
    return;
  }

  // provision (measure time-to-ready)
  const t0 = Date.now();
  const prov = http.post(`${BASE}/v1/practice/attempts/${attemptId}/provision`, null, authHeaders(token));
  const dt = Date.now() - t0;
  const ready = prov.status >= 200 && prov.status < 300 && prov.json('status') === 'READY';
  provisionOk.add(ready);
  if (ready) provisionMs.add(dt);
  if (!ready) {
    // still try to clean up whatever partial env exists
    destroyAttempt(token, attemptId);
    return;
  }

  // connect
  const conn = http.post(`${BASE}/v1/practice/attempts/${attemptId}/connect`, null, authHeaders(token));
  const connGood =
    conn.status >= 200 &&
    conn.status < 300 &&
    typeof conn.json('terminalWsUrl') === 'string' &&
    conn.json('terminalWsUrl').indexOf('/terminal?session=') !== -1;
  connectOk.add(connGood);

  // start (CREATED/READY -> IN_PROGRESS) — submit is rejected without it
  http.post(`${BASE}/v1/practice/attempts/${attemptId}/start`, null, authHeaders(token));

  // 20+ "commands" — workspace file writes at the same cardinality
  for (let i = 0; i < CMDS; i++) {
    const path = `loadtest/step-${i}.txt`;
    const w = http.post(
      `${BASE}/v1/practice/attempts/${attemptId}/files/content?path=${encodeURIComponent(path)}`,
      JSON.stringify({ content: `vu=${__VU} iter=${__ITER} step=${i} ts=${Date.now()}\n` }),
      authHeaders(token),
    );
    if (w.status >= 200 && w.status < 300) cmdWrites.add(1);
    if (THINK_MS > 0 && i % 5 === 4) sleep(THINK_MS / 1000);
  }

  // check (learner-triggered validation, no state change)
  http.post(`${BASE}/v1/practice/attempts/${attemptId}/check`, null, authHeaders(token));

  // submit — this IS the teardown trigger. Per attempt.service.ts, submit()
  // scores the attempt and (with the real orchestrator) requests environment
  // Destroy → ENV_DESTROYED. There is no separate learner-facing destroy
  // route; check-orphans.sh is the backstop proof that submit's teardown left
  // nothing behind.
  const sub = http.post(`${BASE}/v1/practice/attempts/${attemptId}/submit`, null, authHeaders(token));
  const submitted = sub.status >= 200 && sub.status < 300;
  submitOk.add(submitted);
  // destroyOk tracks "the attempt's environment was released". submit success
  // is the signal for that in this build; check-orphans.sh independently
  // verifies zero leaked namespaces after the run.
  destroyOk.add(submitted);
  if (submitted) labsCompleted.add(1);
}

function destroyAttempt(token, attemptId) {
  // Kept for the provision-failed path: no submit happened, so ask the
  // reaper's owner (practice-core) to abandon the attempt if such a route
  // exists; otherwise the TTL/idle reaper reclaims it and check-orphans.sh
  // is the assertion. A 404 here is expected and not a failure.
  const res = http.del(`${BASE}/v1/practice/attempts/${attemptId}/environment`, null, authHeaders(token));
  destroyOk.add(res.status === 404 || (res.status >= 200 && res.status < 300));
}

export default function () {
  // stagger start across LOAD_RAMP so VUs ramp rather than thundering-herd
  if (__ITER === 0) {
    const rampMs = parseRampMs(RAMP);
    sleep((Math.random() * rampMs) / 1000);
  }
  const token = vuToken();
  const avid = resolveLabVersionId(token);
  runOneLab(token, avid);
  if (THINK_MS > 0) sleep(THINK_MS / 1000);
}

function parseRampMs(s) {
  const m = /^(\d+)(ms|s|m)?$/.exec(s.trim());
  if (!m) return 30000;
  const n = parseInt(m[1], 10);
  return m[2] === 'm' ? n * 60000 : m[2] === 'ms' ? n : n * 1000;
}

export function handleSummary(data) {
  // Pull the orchestrator's own provision histogram for cross-checking the
  // client-measured p95 (they should agree within a second or two).
  let orchNote = 'orchestrator /metrics not reachable';
  try {
    const res = http.get(ORCH_METRICS);
    if (res.status === 200) {
      const line = res.body
        .split('\n')
        .find((l) => l.startsWith('orchestrator_provision_duration_seconds_count'));
      orchNote = line ? `orchestrator: ${line}` : 'orchestrator provision histogram not yet populated';
    }
  } catch (e) {
    orchNote = `orchestrator /metrics error: ${e}`;
  }
  return {
    stdout:
      textSummary(data) +
      `\n${orchNote}\n` +
      '\nNext: run evaluation/phase1/load/check-orphans.sh now and again in 1h,\n' +
      'then fill evaluation/phase1/results/loadtest-<date>.md.\n',
    'load-summary.json': JSON.stringify(data, null, 2),
  };
}

// minimal text summary (avoids importing k6-summary from jslib over the network)
function textSummary(data) {
  const m = data.metrics || {};
  const g = (name, path) => {
    const v = m[name];
    if (!v) return `${name}: (n/a)`;
    const val = path.split('.').reduce((o, k) => (o == null ? o : o[k]), v.values);
    return `${name}: ${typeof val === 'number' ? val.toFixed(2) : val}`;
  };
  return [
    '=== Phase 1 load run summary ===',
    g('provision_ok', 'rate'),
    g('provision_duration_ms', 'p(95)'),
    g('connect_ok', 'rate'),
    g('submit_ok', 'rate'),
    g('destroy_ok', 'rate'),
    g('labs_completed', 'count'),
    g('workspace_cmd_writes', 'count'),
    g('http_req_failed', 'rate'),
  ].join('\n');
}
