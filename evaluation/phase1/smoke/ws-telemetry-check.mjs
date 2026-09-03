// IP2b end-to-end check: real WS terminal -> Session Broker telemetry tap
// -> NATS env.telemetry.COMMAND_EXECUTED -> (asserted separately in the
// practice-core event store).
//
// Provisions a T1 env via the orchestrator gRPC (grpcurl), opens the
// terminal WebSocket the same way a browser would, types real commands,
// and prints the COMMAND_EXECUTED payloads seen on NATS.
//
// Usage: node evaluation/phase1/smoke/ws-telemetry-check.mjs
// Requires: grpcurl, a running `docker compose --profile app` stack,
//           orchestrator gRPC on :50051, WS gateway on :8081, NATS on :4222.

import { execFileSync } from 'node:child_process';
import { connect as natsConnect } from '/Users/tanya/Desktop/Hands-On Lab/practice-core/node_modules/@nats-io/transport-node/index.js';

const dec = new TextDecoder();

const ORCH = 'localhost:50051';
const SECRET = 'compose-dev-shared-secret';
const ATTEMPT = process.env.ATTEMPT_ID ?? crypto.randomUUID();

function grpc(method, body) {
  const out = execFileSync('grpcurl', [
    '-plaintext',
    '-H', `authorization: Bearer ${SECRET}`,
    '-d', JSON.stringify(body),
    ORCH, `practiceengine.orchestrator.v1.EnvironmentOrchestrator/${method}`,
  ], { encoding: 'utf8' });
  return JSON.parse(out);
}

const nc = await natsConnect({ servers: 'localhost:4222' });
const sub = nc.subscribe('env.telemetry.COMMAND_EXECUTED');
const seen = [];
(async () => {
  for await (const m of sub) {
    seen.push(JSON.parse(dec.decode(m.data)));
  }
})();

console.log(`[provision] attempt=${ATTEMPT}`);
const prov = grpc('Provision', {
  attempt_id: ATTEMPT,
  blueprint_id: 'linux-tools',
  blueprint_version: 'v1',
  tier: 'TIER_T1_SHARED_CONTAINER',
  ttl_minutes: 15,
  idle_timeout_minutes: 10,
});
console.log(`[provision] status=${prov.status} env=${prov.environmentId}`);
const wsUrl = prov.endpoints.terminalWsUrl;

const ws = new WebSocket(wsUrl);
await new Promise((res, rej) => {
  ws.onopen = res;
  ws.onerror = rej;
});
console.log('[ws] connected');

const cmds = ['echo telemetry-probe-one', 'true', 'false', 'ls /nonexistent-xyz', 'whoami'];
for (const c of cmds) {
  ws.send(c + '\n');
  await new Promise((r) => setTimeout(r, 900));
}
await new Promise((r) => setTimeout(r, 2500));
ws.close();

console.log(`\n[nats] COMMAND_EXECUTED events seen: ${seen.length}`);
for (const e of seen) {
  console.log('  ', JSON.stringify(e));
}

grpc('Destroy', { environment_id: prov.environmentId, reason: 'admin', attempt_id: ATTEMPT });
console.log('[destroy] sent');
await nc.drain();

const ok = seen.some((e) => (e.payload?.cmd ?? '').includes('telemetry-probe-one'))
  && seen.some((e) => e.payload?.exit_code === 1);
console.log(`\nRESULT: ${ok ? 'PASS' : 'FAIL'} (captured command text + non-zero exit code over NATS)`);
process.exit(ok ? 0 : 1);
