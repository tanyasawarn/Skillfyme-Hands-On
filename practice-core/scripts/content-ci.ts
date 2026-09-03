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
 *   5. TIMING: total wall-clock time from Provision through the last
 *      flake run, reported per activity -- a proxy for whether the
 *      activity will meet a learner-facing SLA, and for whether solution
 *      scripts or validators have crept slow enough to be worth
 *      investigating.
 *   6. COST: estimated USD cost of the run just measured, using the same
 *      T1 hourly-rate estimate the real orchestrator's cost meter uses
 *      (internal/costmeter.hourlyRateUSD, doc §5.2's $0.04/hr T1
 *      midpoint -- duplicated here deliberately rather than imported,
 *      since content-core has no dependency on the Go orchestrator's
 *      internal packages; keep the two constants in sync by hand if the
 *      rate ever changes). Flagged if it exceeds DEFAULT_BUDGET_USD
 *      (doc §13.1 exit criterion: cost/attempt < $0.08) -- this measures
 *      only the CI harness's own provision-through-flake run, not a
 *      real learner attempt's actual duration, so it's a lower-bound
 *      signal ("this activity is already over budget just to verify
 *      it"), not a precise prediction of real attempt cost.
 *   7. Destroy the environment.
 *
 * Activities without reference_solution.repo_path are skipped (reported,
 * not failed) -- most content/activities/*.yaml don't have one authored
 * yet, same "report what's missing" stance lint-content.ts takes for
 * schema gaps.
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/content-ci.ts [activity-id]
 * Env: ORCHESTRATOR_GRPC_ADDRESS (default localhost:50051), CI_FLAKE_RUNS (default 5,
 *      matching this file's own "flake x5" spec above -- previously defaulted to 3, a
 *      real spec/implementation drift caught during this session's remediation pass),
 *      ORCHESTRATOR_SHARED_SECRET (bearer token, required once the orchestrator has
 *      auth enabled -- see internal/orchestrator/auth.go), CI_BUDGET_USD (default 0.08,
 *      matches DEFAULT_BUDGET_USD)
 */
import * as fs from 'node:fs';
import * as path from 'node:path';
import { randomUUID } from 'node:crypto';
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

interface SeedRef {
  fixture: string;
  version?: string | number;
}

interface ActivitySpec {
  id: string;
  environment: { tier: string; blueprint: string; seed?: SeedRef[] };
  tasks: TaskSpec[];
  reference_solution?: { repo_path: string };
}

const TIER_MAP: Record<string, string> = {
  BROWSER: 'TIER_T0_BROWSER',
  SHARED_CONTAINER: 'TIER_T1_SHARED_CONTAINER',
  ISOLATED_VM: 'TIER_T2_ISOLATED_MICROVM',
  CLOUD_ACCOUNT: 'TIER_T3_CLOUD_ACCOUNT',
};

const FLAKE_RUNS = Number(process.env.CI_FLAKE_RUNS ?? 5);
const ACTIVITIES_DIR = path.resolve(__dirname, '../../content/activities');

// Mirrors orchestrator/internal/costmeter.hourlyRateUSD -- see this
// file's header doc comment on why it's duplicated rather than shared.
const T1_HOURLY_RATE_USD = 0.04;
const CI_BUDGET_USD = Number(process.env.CI_BUDGET_USD ?? 0.08);

class OrchestratorRpc {
  private client: any;
  private sharedSecret: string | undefined;

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
    this.sharedSecret = process.env.ORCHESTRATOR_SHARED_SECRET || undefined;
  }

  call<TReq, TRes>(method: string, request: TReq, deadlineMs = 60_000): Promise<TRes> {
    return new Promise((resolve, reject) => {
      const deadline = new Date(Date.now() + deadlineMs);
      const metadata = new grpc.Metadata();
      if (this.sharedSecret) {
        metadata.set('authorization', `Bearer ${this.sharedSecret}`);
      }
      this.client[method](request, metadata, { deadline }, (err: grpc.ServiceError | null, response: TRes) => {
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
  attemptId: string,
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
        attemptId,
      });
      results.push({ id: v.id, status: response.status, errorMessage: response.errorMessage || undefined });
    }
  }
  return results;
}

async function applySolutions(rpc: OrchestratorRpc, environmentId: string, attemptId: string, tasks: TaskSpec[], repoPath: string) {
  for (const task of tasks) {
    if (!task.solution_apply) continue;
    const scriptPath = path.join(ACTIVITIES_DIR, repoPath, task.solution_apply);
    if (!fs.existsSync(scriptPath)) {
      throw new Error(`solution_apply script not found: ${scriptPath}`);
    }
    const script = fs.readFileSync(scriptPath, 'utf-8');
    // 90s, not 30s: a reference solution legitimately does more than a
    // single kubectl call -- e.g. lab.k8s.storage's t2 deletes and
    // recreates a Pod twice with readiness waits in between to prove PVC
    // data survives a Pod restart. 30s is a learner-typing-speed budget,
    // not a solution-script budget. The outer RPC deadline tracks it.
    const response = await rpc.call<any, any>(
      'ExecShell',
      { environmentId, command: script, timeoutMs: 90_000, attemptId },
      95_000,
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

type SolutionState =
  | { kind: 'runnable' }
  | { kind: 'none'; reason: string }
  | { kind: 'partial'; reason: string };

/**
 * Decide whether an activity has a golden path content-CI can actually
 * run: it must declare `reference_solution.repo_path` AND every task
 * that declares `solution_apply` must have that script present on disk.
 */
function classifySolution(spec: ActivitySpec): SolutionState {
  if (!spec.reference_solution?.repo_path) {
    return { kind: 'none', reason: 'no reference_solution.repo_path authored' };
  }
  const repoPath = spec.reference_solution.repo_path;
  const declared = spec.tasks.filter((t) => t.solution_apply);
  if (declared.length === 0) {
    return {
      kind: 'none',
      reason: 'reference_solution.repo_path declared but no task has solution_apply',
    };
  }
  const missing = declared
    .map((t) => t.solution_apply as string)
    .filter((rel) => !fs.existsSync(path.join(ACTIVITIES_DIR, repoPath, rel)));
  if (missing.length > 0) {
    return {
      kind: 'partial',
      reason: `solution_apply script(s) missing on disk: ${missing.join(', ')}`,
    };
  }
  return { kind: 'runnable' };
}

async function checkActivity(
  rpc: OrchestratorRpc,
  file: string,
  explicitlyRequested: boolean,
): Promise<boolean> {
  const spec = loadActivity(file);

  // Two shapes of "this activity has no runnable golden path":
  //
  //  a) no reference_solution.repo_path at all -- nothing authored yet.
  //  b) repo_path is declared, but a task's solution_apply script file
  //     is missing on disk -- half-authored.
  //
  // On a full-library run both are a SKIP (report, don't fail -- most
  // activities are still un-authored, tracked in
  // PHASE0_1_2_PENDING_CLOSEOUT.md Track 2C). But when an activity was
  // named explicitly (a per-PR run on a changed activity, or a manual
  // dispatch), the same situation is a FAILURE: you asked to verify
  // this one and it can't be verified.
  const solutionState = classifySolution(spec);
  if (solutionState.kind !== 'runnable') {
    const msg = `${spec.id} (${solutionState.reason})`;
    if (explicitlyRequested) {
      console.error(`FAIL  ${msg} -- explicitly requested but has no runnable golden path`);
      return false;
    }
    console.log(`SKIP  ${msg}`);
    return true;
  }
  // classifySolution guaranteed this is set for kind === 'runnable'.
  const repoPath = spec.reference_solution!.repo_path;

  console.log(`\n=== ${spec.id} ===`);
  let ok = true;
  const startedAt = Date.now();

  // Captured once and threaded through every subsequent RPC in this
  // function -- PLAN_RPC_AUTHZ.md Section 7's cleanup sweep found this
  // script constructs its own separate OrchestratorRpc client (bypasses
  // OrchestratorClient/GrpcOrchestratorClient entirely), so it needed
  // its own fix for the new attempt_id ownership check
  // (orchestrator/internal/orchestrator/server.go's
  // checkEnvironmentOwnership) rather than inheriting Section 4's.
  //
  // Must be a real UUID, not the old `content-ci-${id}-${Date.now()}`
  // debug-friendly string: env.environment.attempt_id is a `uuid`
  // column, and Provision's own INSERT is best-effort (a write failure
  // there only logs a WARNING server-side, never fails the RPC --
  // server.go's own comment on that INSERT). The old non-UUID value
  // silently failed that INSERT every single run, leaving env.environment
  // with NO row for the provisioned environment at all -- invisible until
  // this ownership check made ANY caller (including the true owner) get
  // rejected with PermissionDenied because there was no owner row to
  // match against. Live-caught while verifying this exact fix end-to-end
  // (a real `gRPC Destroy failed: 7 PERMISSION_DENIED` against the live
  // orchestrator), not a hypothetical.
  const attemptId = randomUUID();

  // Pass the activity's declared seed fixtures through to Provision (proto
  // ProvisionRequest.fixtures, field 6). Without this the env is bare --
  // e.g. a K8S_ASSERT lab seeded with fx.k3s-ready.v1 never gets the
  // in-pod ~/.kube/config that fixture writes, so its solution_apply's
  // `kubectl apply` can't run and the golden path fails for a reason that
  // has nothing to do with the solution or validator. server.go's
  // fixture.ApplyAll already tolerates an unimplemented fixture id (logs
  // a WARNING, continues), so forwarding every seed entry is safe even
  // for activities whose fixtures don't have handlers yet.
  const fixtures = (spec.environment.seed ?? [])
    .filter((s) => s && s.fixture)
    .map((s) => ({
      fixtureId: s.fixture,
      version: s.version != null ? String(s.version) : '1',
    }));
  if (fixtures.length > 0) {
    console.log(`  seed fixtures: ${fixtures.map((f) => f.fixtureId).join(', ')}`);
  }

  const provisionResp = await rpc.call<any, any>(
    'Provision',
    {
      attemptId,
      blueprintId: spec.environment.blueprint,
      blueprintVersion: '1',
      tier: TIER_MAP[spec.environment.tier] ?? 'TIER_T1_SHARED_CONTAINER',
      fixtures,
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
    const nullResults = await runValidators(rpc, environmentId, attemptId, spec.tasks);
    const falsePasses = nullResults.filter((r) => r.status === 'PASS');
    if (falsePasses.length > 0) {
      ok = false;
      console.error(`  NULL PATH FAIL: validators passed with no work done: ${falsePasses.map((r) => r.id).join(', ')}`);
    } else {
      console.log(`  null path OK: all ${nullResults.length} validators correctly FAIL/ERROR on untouched env`);
    }

    // --- golden path: apply solutions, every validator should PASS ---
    await applySolutions(rpc, environmentId, attemptId, spec.tasks, repoPath);
    const goldenResults = await runValidators(rpc, environmentId, attemptId, spec.tasks);
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
      runs.push(await runValidators(rpc, environmentId, attemptId, spec.tasks));
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

    // --- timing + cost: measured from provision through the last flake
    // run, i.e. everything above -- deliberately excludes Destroy()
    // itself below, since a real learner attempt's cost accrual also
    // stops being billed at the moment teardown starts, not after it
    // completes (matches costmeter.StopMetering's call site in
    // destroyer.go, which runs before the namespace delete finishes).
    const elapsedMs = Date.now() - startedAt;
    const elapsedHours = elapsedMs / (1000 * 60 * 60);
    const estimatedCostUsd = elapsedHours * T1_HOURLY_RATE_USD;
    console.log(`  timing: ${(elapsedMs / 1000).toFixed(1)}s (provision through flake run ${FLAKE_RUNS})`);
    console.log(`  cost: ~$${estimatedCostUsd.toFixed(4)} (T1 @ $${T1_HOURLY_RATE_USD}/hr estimate, budget ceiling $${CI_BUDGET_USD})`);
    if (estimatedCostUsd > CI_BUDGET_USD) {
      ok = false;
      console.error(`  COST OVER BUDGET: ~$${estimatedCostUsd.toFixed(4)} exceeds $${CI_BUDGET_USD} ceiling -- this activity's CI verification run alone already costs more than the whole-attempt budget; a real learner attempt (slower, more exploration) will cost more, not less`);
    }
  } finally {
    await rpc.call('Destroy', { environmentId, reason: 'admin', attemptId });
    console.log(`  destroyed env=${environmentId}`);
  }

  return ok;
}

/**
 * Args: zero or more activity selectors, as separate argv entries and/or
 * a single comma-separated entry. A selector matches a file if it is a
 * substring of the filename (so `lab.docker.basics`, `docker.basics`, or
 * the full `lab.docker.basics.yaml` all work).
 *
 *   content-ci.ts                          -> full library (un-authored activities SKIP)
 *   content-ci.ts lab.docker.basics        -> just that one, and FAIL if it has no golden path
 *   content-ci.ts a.yaml b.yaml            -> those two
 *   content-ci.ts a,b,c                    -> those three
 *
 * Exit non-zero if ANY selected activity fails a stage, or (for
 * explicitly-selected activities) has no runnable golden path, or if a
 * selector matched no file at all.
 */
function parseSelectors(argv: string[]): string[] {
  return argv
    .flatMap((a) => a.split(','))
    .map((s) => s.trim())
    .filter(Boolean);
}

async function main() {
  const rpc = new OrchestratorRpc();
  const selectors = parseSelectors(process.argv.slice(2));
  const explicit = selectors.length > 0;

  const allFiles = fs
    .readdirSync(ACTIVITIES_DIR)
    .filter((f) => f.endsWith('.yaml'));

  let files: string[];
  if (!explicit) {
    files = allFiles;
  } else {
    files = [];
    let missingSelector = false;
    for (const sel of selectors) {
      const matched = allFiles.filter((f) => f.includes(sel));
      if (matched.length === 0) {
        console.error(`ERROR: selector '${sel}' matched no activity file under ${ACTIVITIES_DIR}`);
        missingSelector = true;
        continue;
      }
      for (const m of matched) if (!files.includes(m)) files.push(m);
    }
    if (missingSelector) {
      console.log('\ncontent-ci: FAIL');
      process.exit(1);
    }
    console.log(`content-ci: ${files.length} activit${files.length === 1 ? 'y' : 'ies'} selected: ${files.join(', ')}`);
  }

  let allOk = true;
  for (const file of files) {
    try {
      const ok = await checkActivity(rpc, file, explicit);
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
