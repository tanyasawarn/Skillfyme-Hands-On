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
});
