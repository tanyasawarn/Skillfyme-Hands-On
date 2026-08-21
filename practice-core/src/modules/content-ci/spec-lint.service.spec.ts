import * as fs from 'node:fs';
import * as path from 'node:path';
import { SpecLintService } from './spec-lint.service';

describe('SpecLintService', () => {
  const service = new SpecLintService();
  const knownSkills = new Set([
    'k8s.deployments',
    'k8s.yaml',
    'kubectl.cli',
    'k8s.services',
    'docker.basics',
    'linux.cli',
    'k8s.core',
    'k8s.architecture',
    'k8s.pods',
  ]);

  it('lints the doc §3.2 worked example (lab.k8s.deploy-node-app.yaml) as valid', () => {
    const yamlPath = path.resolve(
      __dirname,
      '../../../../content/activities/lab.k8s.deploy-node-app.yaml',
    );
    const source = fs.readFileSync(yamlPath, 'utf-8');
    const spec = service.parseYaml(source);
    const result = service.lint(spec, knownSkills);
    if (!result.valid) {
      console.error(JSON.stringify(result.issues, null, 2));
    }
    expect(result.valid).toBe(true);
    expect(result.issues).toEqual([]);
  });

  it('rejects a spec missing on_fail text on a validator (doc §3.2: required CI-lint field)', () => {
    const spec = minimalValidSpec();
    (spec.tasks[0].validators[0] as Record<string, unknown>).on_fail =
      undefined;
    delete (spec.tasks[0].validators[0] as Record<string, unknown>).on_fail;
    const result = service.lint(spec, knownSkills);
    expect(result.valid).toBe(false);
    expect(result.issues.some((i) => i.message.includes('on_fail'))).toBe(true);
  });

  it('rejects a task with zero validators (doc §3.5 step 1)', () => {
    const spec = minimalValidSpec();
    spec.tasks[0].validators = [];
    const result = service.lint(spec, knownSkills);
    expect(result.valid).toBe(false);
  });

  it('rejects a spec referencing an unknown skill slug', () => {
    const spec = minimalValidSpec();
    spec.skills[0].skill = 'totally.made.up.skill';
    const result = service.lint(spec, knownSkills);
    expect(result.valid).toBe(false);
    expect(
      result.issues.some((i) => i.message.includes('unknown skill slug')),
    ).toBe(true);
  });

  it('rejects a spec referencing an unknown prerequisite skill slug', () => {
    const spec = minimalValidSpec();
    spec.prerequisites = { hard: ['nonexistent.skill'] };
    const result = service.lint(spec, knownSkills);
    expect(result.valid).toBe(false);
    expect(
      result.issues.some((i) => i.message.includes('unknown prerequisite')),
    ).toBe(true);
  });
});

function minimalValidSpec(): any {
  return {
    id: 'lab.test.minimal',
    version: 1,
    mode: 'GUIDED_LAB',
    status: 'DRAFT',
    meta: {
      title: 'Test',
      summary: 'Test',
      difficulty_level: 'L1',
      estimated_minutes: 10,
    },
    curriculum: { primary_topic: 'topic.test' },
    skills: [{ skill: 'docker.basics', weight: 1.0, primary: true }],
    environment: {
      tier: 'SHARED_CONTAINER',
      blueprint: 'bp.test.v1',
      cost_budget_usd: 0.01,
    },
    tasks: [
      {
        key: 't1',
        title: 'Task 1',
        required: true,
        instructions_md: 'Do the thing.',
        validators: [
          {
            id: 'v.thing',
            type: 'SHELL_ASSERT',
            run: 'true',
            expect: { exit_code: 0 },
            weight: 1.0,
            on_fail: 'The thing did not happen.',
          },
        ],
      },
    ],
    completion: { rule: 'ALL_REQUIRED_TASKS_PASS' },
    scoring: { profile: 'sp.guided-lab.default' },
  };
}
