import * as fs from 'node:fs';
import * as path from 'node:path';
import * as yaml from 'js-yaml';
import { SpecLintService } from '../content-ci/spec-lint.service';
import type { ActivitySpec } from './activity-spec';

/**
 * PLAN.md Phase 3's K10: proves `ActivitySpec` is a faithful mirror of
 * contracts/activity_spec.schema.json, not just a plausible-looking
 * guess -- parses a real, real-content YAML activity, runs it through
 * the actual Ajv-compiled schema (SpecLintService, the same validator
 * CI/the CMS use), asserts it is genuinely schema-valid, then checks the
 * parsed object satisfies `ActivitySpec` with only an `unknown` ->
 * `ActivitySpec` cast (no field-by-field massaging) -- if a required
 * field on the type didn't match the schema's own `required` array,
 * this cast would still compile at runtime (TypeScript can't check a
 * plain assertion against real data), so the meaningful check is that
 * `result.valid` is true against real content using every optional
 * section (health_gate is the one section no real content file uses
 * yet, so it's exercised separately by structural-only construction
 * below).
 */
describe('ActivitySpec', () => {
  const lint = new SpecLintService();

  it('accepts a real, schema-valid activity YAML with zero lint issues', () => {
    const yamlPath = path.resolve(
      __dirname,
      '../../../../content/activities/lab.linux.navigate-filesystem.yaml',
    );
    const raw = fs.readFileSync(yamlPath, 'utf-8');
    const parsed = yaml.load(raw);

    const result = lint.lint(parsed, new Set(['linux.cli']));
    expect(result.valid).toBe(true);
    expect(result.issues).toEqual([]);

    const spec = parsed as ActivitySpec;
    expect(spec.id).toBe('lab.linux.navigate-filesystem');
    expect(spec.mode).toBe('GUIDED_LAB');
    expect(spec.tasks.length).toBeGreaterThan(0);
    expect(spec.tasks[0].validators.length).toBeGreaterThan(0);
    expect(spec.meta.difficulty_level).toBe('L1');
    expect(spec.completion.rule).toBe('ALL_REQUIRED_TASKS_PASS');
  });

  it('rejects a spec missing a schema-required field (positive control)', () => {
    const yamlPath = path.resolve(
      __dirname,
      '../../../../content/activities/lab.linux.navigate-filesystem.yaml',
    );
    const raw = fs.readFileSync(yamlPath, 'utf-8');
    const parsed = yaml.load(raw) as Record<string, unknown>;
    delete parsed.completion;

    const result = lint.lint(parsed, new Set(['linux.cli']));
    expect(result.valid).toBe(false);
    expect(result.issues.some((i) => i.message.includes('completion'))).toBe(
      true,
    );
  });

  it('a fully-populated PRODUCTION_SIM-only spec section (health_gate/faults/process_signals/artifacts_required) type-checks against the real schema', () => {
    // No real content file uses these sections yet (they were added to
    // the schema ahead of the content that will use them, per the
    // schema's own doc comments) -- constructed directly here so the
    // type is checked against the schema's real shape for these fields
    // even without a fixture file exercising them end-to-end.
    const spec: ActivitySpec = {
      id: 'lab.test.prod-sim-fixture',
      version: 1,
      mode: 'PRODUCTION_SIM',
      status: 'DRAFT',
      meta: {
        title: 't',
        summary: 's',
        difficulty_level: 'L3',
        estimated_minutes: 25,
      },
      curriculum: { primary_topic: 'topic.test' },
      skills: [{ skill: 'test.skill', weight: 1, primary: true }],
      environment: {
        tier: 'SHARED_CONTAINER',
        blueprint: 'bp.test',
        cost_budget_usd: 0.03,
      },
      health_gate: [
        {
          type: 'HTTP_PROBE',
          url: 'http://localhost/health',
          expect_status: 200,
        },
      ],
      faults: [{ id: 'f.test.fault', apply_at: 'T0' }],
      process_signals: {
        diagnostic_efficiency: { good_actions: ['a'], bad_actions: ['b'] },
        blast_radius: { forbidden: ['rm -rf'] },
      },
      artifacts_required: [
        { key: 'incident-note', type: 'MARKDOWN', rubric: 'rub.test' },
      ],
      tasks: [
        {
          key: 't1',
          title: 'Task 1',
          required: true,
          instructions_md: 'do the thing',
          validators: [
            {
              id: 'v1',
              type: 'SHELL_ASSERT',
              expect: {},
              weight: 1,
              on_fail: 'try again',
            },
          ],
        },
      ],
      completion: { rule: 'ALL_REQUIRED_TASKS_PASS' },
      scoring: { profile: 'sp.production-sim.default' },
    };

    const result = lint.lint(spec, new Set(['test.skill']));
    expect(result.valid).toBe(true);
    expect(result.issues).toEqual([]);
  });

  it('a fully-populated PROJECT-mode spec (milestones/defence/cloud/validator config) type-checks against the real schema', () => {
    // Phase 3 (0.9). No real content file is PROJECT mode yet -- the
    // schema + this TS mirror land ahead of the content that will use
    // them, same pattern as the PRODUCTION_SIM section above. Constructed
    // directly so ActivitySpec's milestones/defence/cloud/validator-config
    // shapes are checked against the schema's real `required` arrays.
    const spec: ActivitySpec = {
      id: 'proj.sre.multi-region-checkout',
      version: 1,
      mode: 'PROJECT',
      status: 'DRAFT',
      meta: {
        title: 'Multi-region checkout platform',
        summary: 'Design, build and defend a resilient checkout stack on AWS.',
        difficulty_level: 'L5',
        estimated_minutes: 720,
      },
      curriculum: { primary_topic: 'topic.sre.resilience' },
      skills: [{ skill: 'aws.architecture', weight: 1, primary: true }],
      environment: {
        tier: 'CLOUD_ACCOUNT',
        blueprint: 'bp.aws.project-base',
        cost_budget_usd: 4.5,
        cloud: {
          regions: ['us-east-1', 'us-west-2'],
          sku_exceptions: [],
        },
      },
      milestones: [
        {
          key: 'design',
          title: 'Architecture design',
          gate: 'RUBRIC_MIN_LEVEL',
          blocking: true,
          environment_required: false,
          task_keys: ['design-doc'],
          rubric: 'rub.architecture.v3',
          min_level: 3,
        },
        {
          key: 'infra',
          title: 'Infrastructure',
          gate: 'BOTH',
          environment_required: true,
          task_keys: ['tf-apply', 'cloud-posture'],
          rubric: 'rub.architecture.v3',
          min_level: 3,
        },
        {
          key: 'hardening',
          title: 'Production hardening',
          gate: 'ALL_VALIDATORS_PASS',
          environment_required: true,
          task_keys: ['chaos', 'perf'],
        },
        {
          key: 'final',
          title: 'Submission & defence',
          gate: 'BOTH',
          environment_required: true,
          task_keys: ['acceptance'],
          rubric: 'rub.reasoning.v1',
          min_level: 3,
        },
      ],
      defence: {
        rubric: 'rub.reasoning.v1',
        num_questions: 8,
        human_review: 'CERTIFICATION_ONLY',
      },
      tasks: [
        {
          key: 'design-doc',
          title: 'Architecture document',
          required: true,
          instructions_md: 'Submit architecture doc + diagram + cost estimate.',
          validators: [
            {
              id: 'v.design.sections',
              type: 'FILE_PARSE',
              run: 'DESIGN.md',
              expect: { required_sections: ['Overview', 'Cost', 'Risks'] },
              weight: 1,
              on_fail: 'DESIGN.md is missing a required section.',
            },
          ],
        },
        {
          key: 'tf-apply',
          title: 'Terraform applied cleanly',
          required: true,
          instructions_md: 'Apply your Terraform to the sandbox account.',
          validators: [
            {
              id: 'v.infra.iac',
              type: 'IAC_STATE',
              expect: {},
              weight: 1,
              on_fail: 'Terraform state has drift or a local backend.',
              config: {
                iac_state: {
                  working_dir: 'infra',
                  no_drift: true,
                  require_remote_backend: true,
                  forbid_secrets_in_state: true,
                },
              },
            },
          ],
        },
        {
          key: 'cloud-posture',
          title: 'Cloud posture',
          required: true,
          instructions_md: 'No public data stores; encryption at rest.',
          validators: [
            {
              id: 'v.infra.cloud',
              type: 'CLOUD_ASSERT',
              expect: {},
              weight: 1,
              on_fail: 'A data store is publicly reachable.',
              config: {
                cloud_assert: {
                  checks: ['no_public_data_stores', 'encryption_at_rest'],
                },
              },
            },
            {
              id: 'v.infra.static',
              type: 'STATIC_ANALYSIS',
              expect: {},
              weight: 1,
              on_fail: 'tfsec found a HIGH severity issue.',
              config: {
                static_analysis: {
                  tool: 'tfsec',
                  target: 'infra',
                  max_severity_allowed: 'MEDIUM',
                },
              },
            },
          ],
        },
        {
          key: 'chaos',
          title: 'Survives a pod kill',
          required: true,
          instructions_md: 'Your service must survive a pod being killed.',
          validators: [
            {
              id: 'v.hard.chaos',
              type: 'CHAOS_PROBE',
              expect: {},
              weight: 1,
              on_fail: 'Service did not recover within the timeout.',
              config: {
                chaos_probe: {
                  action: 'kill_pod',
                  target_selector: 'app=checkout',
                  health_check: {
                    url: 'http://checkout/health',
                    expect_status: 200,
                  },
                  recovery_timeout_ms: 60000,
                },
              },
            },
          ],
        },
        {
          key: 'perf',
          title: 'Meets the latency target',
          required: true,
          instructions_md: 'p95 under 300ms at 50 rps.',
          validators: [
            {
              id: 'v.hard.perf',
              type: 'PERF_BENCH',
              expect: {},
              weight: 1,
              on_fail: 'p95 exceeded the target.',
              config: {
                perf_bench: {
                  target_url: 'http://checkout/',
                  rps: 50,
                  duration_s: 60,
                  p95_ms_max: 300,
                },
              },
            },
          ],
        },
        {
          key: 'acceptance',
          title: 'Full acceptance suite',
          required: true,
          instructions_md:
            'The repo test suite must pass against the live system.',
          validators: [
            {
              id: 'v.final.tests',
              type: 'TEST_SUITE',
              expect: {},
              weight: 1,
              on_fail: 'The acceptance suite has failures.',
              config: {
                test_suite: {
                  command: 'npm test',
                  report_format: 'junit',
                  report_path: 'reports/junit.xml',
                },
              },
            },
          ],
        },
      ],
      completion: { rule: 'ALL_REQUIRED_TASKS_PASS' },
      scoring: { profile: 'sp.project.default' },
    };

    const result = lint.lint(spec, new Set(['aws.architecture']));
    expect(result.valid).toBe(true);
    expect(result.issues).toEqual([]);
  });

  it('rejects a milestone with an unknown key (schema enum)', () => {
    // Start from the known-good PROJECT spec used above, then corrupt one
    // milestone key so the ONLY schema violation is the enum -- proves
    // the new `milestones[].key` enum is actually wired into the schema.
    const yamlOk: ActivitySpec = {
      id: 'proj.test.enum',
      version: 1,
      mode: 'PROJECT',
      status: 'DRAFT',
      meta: {
        title: 't',
        summary: 's',
        difficulty_level: 'L5',
        estimated_minutes: 600,
      },
      curriculum: { primary_topic: 'topic.test' },
      skills: [{ skill: 'test.skill', weight: 1, primary: true }],
      environment: {
        tier: 'CLOUD_ACCOUNT',
        blueprint: 'bp.test',
        cost_budget_usd: 4.5,
      },
      milestones: [
        {
          key: 'design',
          title: 'Design',
          gate: 'ALL_VALIDATORS_PASS',
          environment_required: false,
          task_keys: ['t1'],
        },
        {
          key: 'infra',
          title: 'Infra',
          gate: 'ALL_VALIDATORS_PASS',
          environment_required: true,
          task_keys: ['t1'],
        },
      ],
      tasks: [
        {
          key: 't1',
          title: 'Task 1',
          required: true,
          instructions_md: 'x',
          validators: [
            {
              id: 'v1',
              type: 'SHELL_ASSERT',
              expect: {},
              weight: 1,
              on_fail: 'x',
            },
          ],
        },
      ],
      completion: { rule: 'ALL_REQUIRED_TASKS_PASS' },
      scoring: { profile: 'sp.project.default' },
    };
    expect(lint.lint(yamlOk, new Set(['test.skill'])).valid).toBe(true);

    const bad = JSON.parse(JSON.stringify(yamlOk)) as Record<string, unknown>;
    (bad.milestones as Array<{ key: string }>)[1].key = 'deployment';
    const result = lint.lint(bad, new Set(['test.skill']));
    expect(result.valid).toBe(false);
    expect(
      result.issues.some((i) => i.path.includes('/milestones/1/key')),
    ).toBe(true);
  });
});
