import type { ValidatorExecutionResult } from '../validator-executor.interface';
import type { ShellRunner } from './shell-runner';
import {
  asArray,
  asRecord,
  asString,
  parseLooseJson,
  pickString,
  type JsonValue,
} from './json-util';

/**
 * Phase 3 (1.8 / B3). STATIC_ANALYSIS validator: runs an IaC/security
 * scanner (tfsec | checkov | trivy) over the learner's repo and gates on
 * an authored severity/count threshold.
 *
 * Config (mirrors ActivitySpecValidatorStaticAnalysisConfig):
 *   tool:                  'tfsec' | 'checkov' | 'trivy'   (required)
 *   target?:               path to scan, relative to the runner (default ".")
 *   max_severity_allowed?: highest severity that does NOT fail the gate
 *                          ('NONE' | 'LOW' | 'MEDIUM' | 'HIGH' | 'CRITICAL'); default 'NONE'
 *   max_findings?:         also fail if the count of findings above
 *                          max_severity_allowed exceeds this
 *
 * Each tool is invoked with JSON output and a non-failing exit so we
 * parse findings ourselves rather than relying on the tool's own gate.
 */
export interface StaticAnalysisConfig {
  tool: 'tfsec' | 'checkov' | 'trivy';
  target?: string;
  max_severity_allowed?: 'NONE' | 'LOW' | 'MEDIUM' | 'HIGH' | 'CRITICAL';
  max_findings?: number;
}

const SEVERITY_ORDER = ['NONE', 'LOW', 'MEDIUM', 'HIGH', 'CRITICAL'] as const;
type Severity = (typeof SEVERITY_ORDER)[number];

function sevRank(s: string): number {
  const i = SEVERITY_ORDER.indexOf(s.toUpperCase() as Severity);
  return i < 0 ? 0 : i;
}

interface Finding {
  ruleId: string;
  severity: string;
  location: string;
  description: string;
}

export async function runStaticAnalysis(
  runner: ShellRunner,
  validatorId: string,
  rawConfig: unknown,
): Promise<ValidatorExecutionResult> {
  const start = Date.now();
  const cfg = (rawConfig ?? {}) as StaticAnalysisConfig;
  const target = cfg.target || '.';
  const maxSev = (cfg.max_severity_allowed || 'NONE').toUpperCase();

  const err = (detail: string): ValidatorExecutionResult => ({
    validatorId,
    status: 'ERROR',
    durationMs: Date.now() - start,
    evidenceRef: detail,
  });

  if (!cfg.tool) {
    return err('STATIC_ANALYSIS validator has no `tool` configured');
  }

  let findings: Finding[];
  try {
    findings = await runTool(runner, cfg.tool, target);
  } catch (e) {
    return err(e instanceof Error ? e.message : String(e));
  }

  const overThreshold = findings.filter(
    (f) => sevRank(f.severity) > sevRank(maxSev),
  );
  const countFail =
    typeof cfg.max_findings === 'number' &&
    overThreshold.length > cfg.max_findings;
  const sevFail = cfg.max_findings === undefined && overThreshold.length > 0;
  const failed = sevFail || countFail;

  const bySeverity: Record<string, number> = {};
  for (const f of findings) {
    const key = f.severity.toUpperCase();
    bySeverity[key] = (bySeverity[key] ?? 0) + 1;
  }

  return {
    validatorId,
    status: failed ? 'FAIL' : 'PASS',
    observed: {
      tool: cfg.tool,
      backend: runner.backend,
      total_findings: findings.length,
      by_severity: bySeverity,
      over_threshold: overThreshold.length,
      sample: overThreshold.slice(0, 10),
    },
    expected: {
      max_severity_allowed: maxSev,
      ...(cfg.max_findings !== undefined
        ? { max_findings: cfg.max_findings }
        : {}),
    },
    durationMs: Date.now() - start,
    evidenceRef: failed
      ? `${overThreshold.length} finding(s) above ${maxSev}` +
        (cfg.max_findings !== undefined ? ` (max ${cfg.max_findings})` : '')
      : undefined,
  };
}

async function runTool(
  runner: ShellRunner,
  tool: StaticAnalysisConfig['tool'],
  target: string,
): Promise<Finding[]> {
  if (tool === 'tfsec') {
    const r = await runner.run({
      argv: ['tfsec', target, '--format', 'json', '--soft-fail', '--no-color'],
      timeoutMs: 180_000,
    });
    if (r.infraError) throw new Error(`tfsec could not run: ${r.infraError}`);
    const parsed = asRecord(parseLooseJson(r.stdout) ?? undefined);
    return asArray(parsed.results).map((raw) => {
      const x = asRecord(raw);
      return {
        ruleId: pickString(x, ['rule_id', 'long_id'], 'unknown'),
        severity: asString(x.severity, 'UNKNOWN'),
        location: locFrom(x.location),
        description: asString(x.description),
      };
    });
  }

  if (tool === 'checkov') {
    const r = await runner.run({
      argv: ['checkov', '-d', target, '-o', 'json', '--compact', '--quiet'],
      timeoutMs: 240_000,
    });
    if (r.infraError) throw new Error(`checkov could not run: ${r.infraError}`);
    // checkov exits non-zero when it finds failures; we parse stdout regardless.
    const parsed = parseLooseJson(r.stdout);
    const blocks = Array.isArray(parsed)
      ? parsed
      : parsed
        ? [parsed]
        : ([] as JsonValue[]);
    const out: Finding[] = [];
    for (const b of blocks) {
      const results = asRecord(asRecord(b).results);
      for (const raw of asArray(results.failed_checks)) {
        const c = asRecord(raw);
        const range = asArray(c.file_line_range);
        const firstLine = range[0];
        const line = typeof firstLine === 'number' ? String(firstLine) : '';
        out.push({
          ruleId: asString(c.check_id, 'unknown'),
          // checkov doesn't emit a severity for every check; default MEDIUM
          // so an un-graded finding still trips a NONE threshold.
          severity: asString(c.severity, 'MEDIUM'),
          location: `${asString(c.file_path)}:${line}`,
          description: asString(c.check_name),
        });
      }
    }
    return out;
  }

  // trivy config scan
  const r = await runner.run({
    argv: ['trivy', 'config', '--format', 'json', '--quiet', target],
    timeoutMs: 240_000,
  });
  if (r.infraError) throw new Error(`trivy could not run: ${r.infraError}`);
  const parsed = asRecord(parseLooseJson(r.stdout) ?? undefined);
  const out: Finding[] = [];
  for (const rawRes of asArray(parsed.Results)) {
    const res = asRecord(rawRes);
    for (const rawM of asArray(res.Misconfigurations)) {
      const m = asRecord(rawM);
      out.push({
        ruleId: asString(m.ID, 'unknown'),
        severity: asString(m.Severity, 'UNKNOWN'),
        location: asString(res.Target),
        description: asString(m.Title),
      });
    }
  }
  return out;
}

function locFrom(loc: JsonValue | undefined): string {
  const l = asRecord(loc);
  const filename = asString(l.filename);
  const startLine = l.start_line;
  return `${filename}:${typeof startLine === 'number' ? startLine : ''}`;
}
