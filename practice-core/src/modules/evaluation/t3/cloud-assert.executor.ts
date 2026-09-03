import type { ValidatorExecutionResult } from '../validator-executor.interface';
import type { ShellRunner } from './shell-runner';
import {
  asArray,
  asBool,
  asRecord,
  asString,
  parseLooseJson,
  type JsonValue,
} from './json-util';

/**
 * Phase 3 (1.8 / B3). CLOUD_ASSERT validator: read-only assertions about
 * the learner's deployed AWS topology, made with a validator-scoped
 * read-only STS role (Stage 3.5 extends MintValidatorCredentials for
 * this; until then LocalShellRunner + a pre-made sandbox account's aws
 * profile is the de-risking path).
 *
 * Config (mirrors ActivitySpecValidatorCloudAssertConfig):
 *   checks:   string[]  — one or more assertion ids (required, >=1)
 *   params?:  free-form per-check parameters (e.g. { region: "us-east-1" })
 *
 * Each check id maps to a small, read-only `aws` CLI invocation and a
 * predicate — the Phase-3 starter set from memory.md §6.2 ("VPC layout,
 * no public data stores, encryption at rest, least-privilege IAM").
 * Unknown check ids are a content-authoring error → the whole validator
 * is ERROR (never scored).
 */
export interface CloudAssertConfig {
  checks: string[];
  params?: Record<string, unknown>;
}

interface CheckOutcome {
  check: string;
  pass: boolean;
  detail: string;
}

type CheckFn = (
  runner: ShellRunner,
  params: Record<string, unknown>,
) => Promise<CheckOutcome>;

export async function runCloudAssert(
  runner: ShellRunner,
  validatorId: string,
  rawConfig: unknown,
): Promise<ValidatorExecutionResult> {
  const start = Date.now();
  const cfg = (rawConfig ?? {}) as CloudAssertConfig;
  const params = cfg.params ?? {};

  const err = (detail: string): ValidatorExecutionResult => ({
    validatorId,
    status: 'ERROR',
    durationMs: Date.now() - start,
    evidenceRef: detail,
  });

  if (!Array.isArray(cfg.checks) || cfg.checks.length === 0) {
    return err('CLOUD_ASSERT validator has no `checks` configured');
  }
  const unknown = cfg.checks.filter((c) => !(c in CHECKS));
  if (unknown.length > 0) {
    return err(
      `CLOUD_ASSERT: unknown check id(s): ${unknown.join(', ')} — valid: ${Object.keys(
        CHECKS,
      ).join(', ')}`,
    );
  }

  const outcomes: CheckOutcome[] = [];
  for (const id of cfg.checks) {
    try {
      outcomes.push(await CHECKS[id](runner, params));
    } catch (e) {
      // A check that could not run (aws CLI missing, creds absent, API
      // error) makes the whole validator ERROR — doc §6.2.
      return err(
        `CLOUD_ASSERT check "${id}" could not run: ${
          e instanceof Error ? e.message : String(e)
        }`,
      );
    }
  }

  const allPass = outcomes.every((o) => o.pass);
  return {
    validatorId,
    status: allPass ? 'PASS' : 'FAIL',
    observed: { backend: runner.backend, checks: outcomes },
    expected: { checks: cfg.checks },
    durationMs: Date.now() - start,
    evidenceRef: allPass
      ? undefined
      : outcomes
          .filter((o) => !o.pass)
          .map((o) => `${o.check}: ${o.detail}`)
          .join('; '),
  };
}

/** Runs an `aws` CLI call with `--output json`; throws on infra failure or non-zero exit. */
async function awsJson(
  runner: ShellRunner,
  argv: string[],
  params: Record<string, unknown>,
): Promise<JsonValue> {
  const region = typeof params.region === 'string' ? params.region : undefined;
  const r = await runner.run({
    argv: ['aws', ...argv, '--output', 'json'],
    env: region
      ? { AWS_REGION: region, AWS_DEFAULT_REGION: region }
      : undefined,
    timeoutMs: 60_000,
  });
  if (r.infraError) throw new Error(r.infraError);
  if (r.exitCode !== 0) {
    throw new Error(
      `aws ${argv.join(' ')} exited ${r.exitCode}: ${r.stderr.slice(0, 300)}`,
    );
  }
  const parsed = parseLooseJson(r.stdout || '{}');
  if (parsed === null) {
    throw new Error(`aws ${argv.join(' ')} did not return JSON`);
  }
  return parsed;
}

const CHECKS: Record<string, CheckFn> = {
  // No S3 bucket is missing a full public-access block.
  no_public_s3: async (runner, params) => {
    const list = asRecord(
      await awsJson(runner, ['s3api', 'list-buckets'], params),
    );
    const buckets = asArray(list.Buckets)
      .map((b) => asString(asRecord(b).Name))
      .filter(Boolean);
    const offenders: string[] = [];
    for (const name of buckets) {
      const pab = await awsJson(
        runner,
        ['s3api', 'get-public-access-block', '--bucket', name],
        params,
      ).catch(() => null);
      const c = asRecord(
        asRecord(pab ?? undefined).PublicAccessBlockConfiguration,
      );
      const blocked =
        asBool(c.BlockPublicAcls) &&
        asBool(c.IgnorePublicAcls) &&
        asBool(c.BlockPublicPolicy) &&
        asBool(c.RestrictPublicBuckets);
      if (!blocked) offenders.push(name);
    }
    return {
      check: 'no_public_s3',
      pass: offenders.length === 0,
      detail:
        offenders.length === 0
          ? `${buckets.length} bucket(s), all with full public-access-block`
          : `bucket(s) without full public-access-block: ${offenders.join(', ')}`,
    };
  },

  // Every RDS instance has StorageEncrypted = true.
  rds_encrypted_at_rest: async (runner, params) => {
    const res = asRecord(
      await awsJson(runner, ['rds', 'describe-db-instances'], params),
    );
    const instances = asArray(res.DBInstances).map(asRecord);
    const offenders = instances
      .filter((i) => !asBool(i.StorageEncrypted))
      .map((i) => asString(i.DBInstanceIdentifier));
    return {
      check: 'rds_encrypted_at_rest',
      pass: offenders.length === 0,
      detail:
        offenders.length === 0
          ? `${instances.length} RDS instance(s), all encrypted at rest`
          : `unencrypted RDS instance(s): ${offenders.join(', ')}`,
    };
  },

  // No security group allows 0.0.0.0/0 to a sensitive port.
  no_world_open_admin_ports: async (runner, params) => {
    const res = asRecord(
      await awsJson(runner, ['ec2', 'describe-security-groups'], params),
    );
    const sensitive = [22, 3306, 5432, 6379, 27017];
    const offenders: string[] = [];
    for (const rawSg of asArray(res.SecurityGroups)) {
      const sg = asRecord(rawSg);
      const groupId = asString(sg.GroupId);
      for (const rawPerm of asArray(sg.IpPermissions)) {
        const perm = asRecord(rawPerm);
        const from = typeof perm.FromPort === 'number' ? perm.FromPort : 0;
        const to = typeof perm.ToPort === 'number' ? perm.ToPort : 65535;
        const worldV4 = asArray(perm.IpRanges).some(
          (r) => asString(asRecord(r).CidrIp) === '0.0.0.0/0',
        );
        if (!worldV4) continue;
        for (const p of sensitive) {
          if (p >= from && p <= to) offenders.push(`${groupId}:${p}`);
        }
      }
    }
    return {
      check: 'no_world_open_admin_ports',
      pass: offenders.length === 0,
      detail:
        offenders.length === 0
          ? 'no security group exposes an admin port to 0.0.0.0/0'
          : `world-open admin port(s): ${offenders.join(', ')}`,
    };
  },

  // A non-default VPC exists (learner built their own).
  non_default_vpc_present: async (runner, params) => {
    const res = asRecord(
      await awsJson(runner, ['ec2', 'describe-vpcs'], params),
    );
    const custom = asArray(res.Vpcs)
      .map(asRecord)
      .filter((v) => !asBool(v.IsDefault))
      .map((v) => asString(v.VpcId));
    return {
      check: 'non_default_vpc_present',
      pass: custom.length > 0,
      detail:
        custom.length > 0
          ? `custom VPC(s): ${custom.join(', ')}`
          : 'only the default VPC is present — no learner-built VPC',
    };
  },

  // No customer-managed policy attached to a role grants "*:*".
  no_wildcard_admin_policy: async (runner, params) => {
    const roles = asRecord(
      await awsJson(runner, ['iam', 'list-roles'], params),
    );
    const offenders: string[] = [];
    for (const rawRole of asArray(roles.Roles)) {
      const role = asRecord(rawRole);
      const roleName = asString(role.RoleName);
      if (asString(role.Path).startsWith('/aws-service-role/')) continue;
      const attached = asRecord(
        await awsJson(
          runner,
          ['iam', 'list-attached-role-policies', '--role-name', roleName],
          params,
        ).catch(() => ({ AttachedPolicies: [] as JsonValue[] })),
      );
      for (const rawP of asArray(attached.AttachedPolicies)) {
        const p = asRecord(rawP);
        const policyArn = asString(p.PolicyArn);
        const policyName = asString(p.PolicyName);
        const pv = asRecord(
          await awsJson(
            runner,
            ['iam', 'get-policy', '--policy-arn', policyArn],
            params,
          ).catch(() => null),
        );
        const versionId = asString(asRecord(pv.Policy).DefaultVersionId);
        if (!versionId) continue;
        const doc = asRecord(
          await awsJson(
            runner,
            [
              'iam',
              'get-policy-version',
              '--policy-arn',
              policyArn,
              '--version-id',
              versionId,
            ],
            params,
          ).catch(() => null),
        );
        const statements = normStatements(
          asRecord(asRecord(doc.PolicyVersion).Document).Statement,
        );
        for (const st of statements) {
          if (
            asString(st.Effect) === 'Allow' &&
            hasWildcard(st.Action) &&
            hasWildcard(st.Resource)
          ) {
            offenders.push(`${roleName}:${policyName}`);
          }
        }
      }
    }
    return {
      check: 'no_wildcard_admin_policy',
      pass: offenders.length === 0,
      detail:
        offenders.length === 0
          ? 'no attached customer-managed policy grants *:*'
          : `role/policy granting *:* — ${offenders.join(', ')}`,
    };
  },
};

function normStatements(
  s: JsonValue | undefined,
): Array<Record<string, JsonValue>> {
  if (Array.isArray(s)) return s.map(asRecord);
  if (s && typeof s === 'object') return [asRecord(s)];
  return [];
}

function hasWildcard(v: JsonValue | undefined): boolean {
  if (v === '*') return true;
  if (Array.isArray(v)) return v.includes('*');
  return false;
}
