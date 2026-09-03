import type { ValidatorExecutionResult } from '../validator-executor.interface';
import type { ShellRunner } from './shell-runner';

/**
 * Phase 3 (1.8 / B3). IAC_STATE validator: assertions about the
 * learner's Terraform state, run inside the T3 workspace (or against a
 * static fixture repo via LocalShellRunner).
 *
 * Config (mirrors ActivitySpecValidatorIacStateConfig, contracts/
 * activity_spec.schema.json):
 *   working_dir?:              Terraform root, relative to the runner (default ".")
 *   no_drift?:                 `terraform plan -detailed-exitcode` must report no changes (exit 0, not 2)
 *   require_remote_backend?:   the state must not be local (`terraform state pull` + backend inspection)
 *   forbid_secrets_in_state?:  the pulled state must not contain obvious secret material
 *
 * Every sub-check that can't be run (binary missing, state unreadable)
 * yields status ERROR for the whole validator — doc §6.2: a validator
 * that itself broke is never scored against the learner.
 */
export interface IacStateConfig {
  working_dir?: string;
  no_drift?: boolean;
  require_remote_backend?: boolean;
  forbid_secrets_in_state?: boolean;
}

// Heuristic secret patterns for forbid_secrets_in_state. Deliberately
// conservative (high-signal) — this is a gate, and a false positive
// blocks a learner. Content authors tighten per-activity via a future
// `secret_patterns` config field if needed.
const SECRET_PATTERNS: Array<{ name: string; re: RegExp }> = [
  { name: 'aws_access_key_id', re: /AKIA[0-9A-Z]{16}/ },
  {
    name: 'private_key_block',
    re: /-----BEGIN (?:RSA |EC |OPENSSH )?PRIVATE KEY-----/,
  },
  {
    // any JSON key ending in "password" / "secret" / "token" with a
    // non-trivial string value (e.g. "db_password", "master_password",
    // "client_secret"). Terraform stores these in plaintext in state.
    name: 'secret_valued_field',
    re: /"[a-z0-9_]*(?:password|secret|token)"\s*:\s*"[^"]{6,}"/i,
  },
];

export async function runIacState(
  runner: ShellRunner,
  validatorId: string,
  rawConfig: unknown,
): Promise<ValidatorExecutionResult> {
  const start = Date.now();
  const cfg = (rawConfig ?? {}) as IacStateConfig;
  const cwd = cfg.working_dir || '.';
  const checks: Array<{ name: string; pass: boolean; detail: string }> = [];

  const err = (detail: string): ValidatorExecutionResult => ({
    validatorId,
    status: 'ERROR',
    durationMs: Date.now() - start,
    evidenceRef: detail,
  });

  // --- terraform init (needs to have run for plan/state to work). We
  // don't run `init` ourselves (it can touch a remote backend); we
  // assume the learner's pipeline did, and surface a clear ERROR if not.
  // --- no_drift: `terraform plan -detailed-exitcode` → exit 0 = no
  // changes, 2 = changes, 1 = error.
  if (cfg.no_drift) {
    const plan = await runner.run({
      argv: [
        'terraform',
        'plan',
        '-detailed-exitcode',
        '-lock=false',
        '-input=false',
        '-no-color',
      ],
      cwd,
      timeoutMs: 180_000,
    });
    if (plan.infraError)
      return err(`terraform plan could not run: ${plan.infraError}`);
    if (plan.exitCode === 1) {
      return err(
        `terraform plan errored (exit 1) — likely no init or a provider error:\n${plan.stderr.slice(0, 800)}`,
      );
    }
    const noDrift = plan.exitCode === 0;
    checks.push({
      name: 'no_drift',
      pass: noDrift,
      detail: noDrift
        ? 'terraform plan reports no changes'
        : `terraform plan reports pending changes (exit ${plan.exitCode})`,
    });
  }

  // --- pull state once for the remaining checks
  const needState = cfg.require_remote_backend || cfg.forbid_secrets_in_state;
  let stateJson = '';
  if (needState) {
    const pull = await runner.run({
      argv: ['terraform', 'state', 'pull'],
      cwd,
      timeoutMs: 60_000,
    });
    if (pull.infraError)
      return err(`terraform state pull could not run: ${pull.infraError}`);
    if (pull.exitCode !== 0) {
      return err(
        `terraform state pull failed (exit ${pull.exitCode}): ${pull.stderr.slice(0, 400)}`,
      );
    }
    stateJson = pull.stdout;
  }

  if (cfg.require_remote_backend) {
    // A local backend leaves a terraform.tfstate file on disk and the
    // pulled state has no "backend" lineage marker distinct from local.
    // The reliable signal: check for a local state file in the working
    // dir. `terraform state pull` succeeds for local too, so the file
    // check is the discriminator.
    const ls = await runner.run({
      argv: [
        'sh',
        '-c',
        'test -f terraform.tfstate && echo LOCAL || echo REMOTE',
      ],
      cwd,
      timeoutMs: 15_000,
    });
    if (ls.infraError)
      return err(`backend check could not run: ${ls.infraError}`);
    const isRemote = ls.stdout.trim() === 'REMOTE';
    checks.push({
      name: 'require_remote_backend',
      pass: isRemote,
      detail: isRemote
        ? 'no local terraform.tfstate present — state is in a remote backend'
        : 'a local terraform.tfstate file is present — state is NOT in a remote backend',
    });
  }

  if (cfg.forbid_secrets_in_state) {
    const hits = SECRET_PATTERNS.filter((p) => p.re.test(stateJson)).map(
      (p) => p.name,
    );
    checks.push({
      name: 'forbid_secrets_in_state',
      pass: hits.length === 0,
      detail:
        hits.length === 0
          ? 'no secret-shaped values found in the pulled state'
          : `possible secrets in state: ${hits.join(', ')}`,
    });
  }

  if (checks.length === 0) {
    return err(
      'IAC_STATE validator has no checks enabled (set no_drift / require_remote_backend / forbid_secrets_in_state)',
    );
  }

  const allPass = checks.every((c) => c.pass);
  return {
    validatorId,
    status: allPass ? 'PASS' : 'FAIL',
    observed: { backend: runner.backend, checks },
    expected: {
      no_drift: cfg.no_drift ?? false,
      require_remote_backend: cfg.require_remote_backend ?? false,
      forbid_secrets_in_state: cfg.forbid_secrets_in_state ?? false,
    },
    durationMs: Date.now() - start,
    evidenceRef: allPass
      ? undefined
      : checks
          .filter((c) => !c.pass)
          .map((c) => `${c.name}: ${c.detail}`)
          .join('; '),
  };
}
