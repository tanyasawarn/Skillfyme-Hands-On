/**
 * Content CI: golden-path / null-path / flake harness (PLAN.md M1.3 step
 * "provision -> golden path -> null path -> flake x5"). Runs against a real
 * environment via the Environment Orchestrator's gRPC contract directly
 * (same dynamic-proto-load pattern as GrpcOrchestratorClient/
 * GrpcValidatorExecutor) -- no NestJS bootstrap, no DB writes, since this
 * is a content-authoring check, not a learner attempt.
 *
 * For each activity YAML under content/activities/ that declares
 * reference_solution.repo_path:
 *   1. Provision a fresh T1_SHARED_CONTAINER environment from its
 *      declared blueprint.
 *   2. NULL PATH: run every task's validators against the untouched
 *      environment. Expect every validator to FAIL (a fresh env has none
 *      of the task's required state yet) -- if any PASS, the validator
 *      is too weak (passes on a learner who did nothing).
 *   3. GOLDEN PATH: apply each task's solution_apply script (via
 *      ExecShell), then re-run every validator. Expect every validator to
 *      PASS -- if any FAIL/ERROR, either the solution script or the
 *      validator itself is broken.
 *   4. FLAKE: repeat the golden-path validator run N times (default 3)
 *      without re-applying the solution. Any validator whose status
 *      differs across runs is flagged as flaky.
 *   5. Destroy the environment.
 *
 * Activities without reference_solution.repo_path are skipped (reported,
 * not failed) -- most content/activities/*.yaml don't have one authored
 * yet, same "report what's missing" stance lint-content.ts takes for
 * schema gaps.
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/content-ci.ts [activity-id]
 * Env: ORCHESTRATOR_GRPC_ADDRESS (default localhost:50051), CI_FLAKE_RUNS (default 3)
 */
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as yaml from 'js-yaml';
import * as grpc from '@grpc/grpc-js';
import * as protoLoader from '@grpc/proto-loader';

interface ValidatorSpec {
  id: string;
  type: string;
  run: string;
  expect: Record<string, unknown>;
  weight: number;
}

interface TaskSpec {
  key: string;
  required: boolean;
  validators: ValidatorSpec[];
  solution_apply?: string;
}

interface ActivitySpec {
  id: string;
  environment: { tier: string; blueprint: string };
  tasks: TaskSpec[];
  reference_solution?: { repo_path: string };
}

const TIER_MAP: Record<string, string> = {
  BROWSER: 'TIER_T0_BROWSER',
  SHARED_CONTAINER: 'TIER_T1_SHARED_CONTAINER',
  ISOLATED_VM: 'TIER_T2_ISOLATED_MICROVM',
  CLOUD_ACCOUNT: 'TIER_T3_CLOUD_ACCOUNT',
};

const FLAKE_RUNS = Number(process.env.CI_FLAKE_RUNS ?? 3);
const ACTIVITIES_DIR = path.resolve(__dirname, '../../content/activities');

class OrchestratorRpc {
  private client: any;

  constructor() {
    const protoPath = path.resolve(__dirname, '../../contracts/orchestrator.proto');
    const packageDefinition = protoLoader.loadSync(protoPath, {
      keepCase: false,
      longs: String,
      enums: String,
      defaults: true,
      oneofs: true,
    });
    const proto = grpc.loadPackageDefinition(packageDefinition) as any;
    const ServiceCtor = proto.practiceengine.orchestrator.v1.EnvironmentOrchestrator;
    const address = process.env.ORCHESTRATOR_GRPC_ADDRESS ?? 'localhost:50051';
    this.client = new ServiceCtor(address, grpc.credentials.createInsecure());
  }

  call<TReq, TRes>(method: string, request: TReq, deadlineMs = 60_000): Promise<TRes> {
    return new Promise((resolve, reject) => {
      const deadline = new Date(Date.now() + deadlineMs);
      this.client[method](request, { deadline }, (err: grpc.ServiceError | null, response: TRes) => {
        if (err) {
          reject(new Error(`gRPC ${method} failed: ${err.code} ${err.message}`));
          return;
        }
        resolve(response);
      });
    });
  }
}

interface ValidatorRunResult {
  id: string;
  status: string;
  errorMessage?: string;
}

async function runValidators(
  rpc: OrchestratorRpc,
  environmentId: string,
  tasks: TaskSpec[],
): Promise<ValidatorRunResult[]> {
  const results: ValidatorRunResult[] = [];
  for (const task of tasks) {
    for (const v of task.validators) {
      const response = await rpc.call<any, any>('ExecValidator', {
        environmentId,
        validatorId: v.id,
        validatorType: v.type,
        run: v.run ?? '',
        expectJson: JSON.stringify(v.expect ?? {}),
        timeoutMs: 30_000,
      });
      results.push({ id: v.id, status: response.status, errorMessage: response.errorMessage || undefined });
    }
  }
  return results;
}

async function applySolutions(rpc: OrchestratorRpc, environmentId: string, tasks: TaskSpec[], repoPath: string) {
  for (const task of tasks) {
    if (!task.solution_apply) continue;
    const scriptPath = path.join(ACTIVITIES_DIR, repoPath, task.solution_apply);
    if (!fs.existsSync(scriptPath)) {
      throw new Error(`solution_apply script not found: ${scriptPath}`);
    }
    const script = fs.readFileSync(scriptPath, 'utf-8');
    const response = await rpc.call<any, any>(
      'ExecShell',
      { environmentId, command: script, timeoutMs: 30_000 },
      35_000,
    );
    if (response.errorMessage) {
      throw new Error(`applying ${task.solution_apply} failed: ${response.errorMessage}`);
    }
    if (response.exitCode !== 0 && response.exitCode !== '0') {
      throw new Error(
        `${task.solution_apply} exited ${response.exitCode}: stdout=${response.stdout} stderr=${response.stderr}`,
      );
    }
  }
}

function loadActivity(file: string): ActivitySpec {
  const source = fs.readFileSync(path.join(ACTIVITIES_DIR, file), 'utf-8');
  return yaml.load(source) as ActivitySpec;
}

async function checkActivity(rpc: OrchestratorRpc, file: string): Promise<boolean> {
  const spec = loadActivity(file);
  if (!spec.reference_solution?.repo_path) {
    console.log(`SKIP  ${spec.id} (no reference_solution.repo_path authored)`);
    return true;
  }

  console.log(`\n=== ${spec.id} ===`);
  let ok = true;

  const provisionResp = await rpc.call<any, any>(
    'Provision',
    {
      attemptId: `content-ci-${spec.id}-${Date.now()}`,
      blueprintId: spec.environment.blueprint,
      blueprintVersion: '1',
      tier: TIER_MAP[spec.environment.tier] ?? 'TIER_T1_SHARED_CONTAINER',
      ttlMinutes: 15,
      idleTimeoutMinutes: 10,
    },
    60_000,
  );

  if (provisionResp.status !== 'ENVIRONMENT_STATUS_READY') {
    console.error(`  PROVISION FAILED: status=${provisionResp.status}`);
    return false;
  }
  const environmentId = provisionResp.environmentId;
  console.log(`  provisioned env=${environmentId}`);

  try {
    // --- null path: untouched env, every validator should FAIL ---
    const nullResults = await runValidators(rpc, environmentId, spec.tasks);
    const falsePasses = nullResults.filter((r) => r.status === 'PASS');
    if (falsePasses.length > 0) {
      ok = false;
      console.error(`  NULL PATH FAIL: validators passed with no work done: ${falsePasses.map((r) => r.id).join(', ')}`);
    } else {
      console.log(`  null path OK: all ${nullResults.length} validators correctly FAIL/ERROR on untouched env`);
    }

    // --- golden path: apply solutions, every validator should PASS ---
    await applySolutions(rpc, environmentId, spec.tasks, spec.reference_solution.repo_path);
    const goldenResults = await runValidators(rpc, environmentId, spec.tasks);
    const falseFails = goldenResults.filter((r) => r.status !== 'PASS');
    if (falseFails.length > 0) {
      ok = false;
      console.error(`  GOLDEN PATH FAIL: validators did not pass after solution applied:`);
      for (const r of falseFails) console.error(`    ${r.id}: ${r.status}${r.errorMessage ? ` (${r.errorMessage})` : ''}`);
    } else {
      console.log(`  golden path OK: all ${goldenResults.length} validators PASS after solution applied`);
    }

    // --- flake: repeat golden-path validator run without reapplying ---
    const runs: ValidatorRunResult[][] = [goldenResults];
    for (let i = 1; i < FLAKE_RUNS; i++) {
      runs.push(await runValidators(rpc, environmentId, spec.tasks));
    }
    const flaky: string[] = [];
    for (const v of spec.tasks.flatMap((t) => t.validators)) {
      const statuses = new Set(runs.map((r) => r.find((x) => x.id === v.id)?.status));
      if (statuses.size > 1) flaky.push(`${v.id} (${[...statuses].join(' / ')})`);
    }
    if (flaky.length > 0) {
      ok = false;
      console.error(`  FLAKE DETECTED across ${FLAKE_RUNS} runs: ${flaky.join(', ')}`);
    } else {
      console.log(`  flake check OK: identical results across ${FLAKE_RUNS} runs`);
    }
  } finally {
    await rpc.call('Destroy', { environmentId, reason: 'admin' });
    console.log(`  destroyed env=${environmentId}`);
  }

  return ok;
}

async function main() {
  const rpc = new OrchestratorRpc();
  const only = process.argv[2];

  const files = fs
    .readdirSync(ACTIVITIES_DIR)
    .filter((f) => f.endsWith('.yaml'))
    .filter((f) => !only || f.includes(only));

  let allOk = true;
  for (const file of files) {
    try {
      const ok = await checkActivity(rpc, file);
      allOk = allOk && ok;
    } catch (err) {
      allOk = false;
      console.error(`  ERROR: ${err instanceof Error ? err.message : err}`);
    }
  }

  console.log(allOk ? '\ncontent-ci: PASS' : '\ncontent-ci: FAIL');
  process.exit(allOk ? 0 : 1);
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
