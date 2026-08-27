/**
 * PLAN.md Phase 3's K10: `ActivitySpec`, the TypeScript mirror of
 * `contracts/activity_spec.schema.json` -- the canonical JSON Schema doc
 * §3.2 calls "the frozen contract for activity_version.spec_jsonb...
 * changes require joint PR approval per PLAN.md 'conflict-minimizing
 * rules'." Before this, `content.activity_version.spec_jsonb` (typed
 * `Jsonb<unknown>` in db/schema.ts -- correctly, since Kysely can't know
 * a JSONB column's shape) was independently re-cast to a different
 * *partial* inline type at 6+ real call sites (`artifact.service.ts`,
 * `catalog.repository.ts`, `evaluation.service.ts`,
 * `command-executed.consumer.ts`, `hint.service.ts`,
 * `attempt.service.ts` x3), each guessing at only the fields it
 * happened to need, with zero shared source of truth -- so a field
 * rename in the real schema had no single place that would fail to
 * compile.
 *
 * Deliberately mirrors the JSON Schema field-for-field (same optional
 * vs. required split as that file's own `required` arrays) rather than
 * inventing a friendlier shape -- this type's only job is to be a
 * faithful, checkable transcription of the one real contract, the same
 * reasoning db/schema.ts's own top comment gives for staying hand-written
 * instead of codegen'd.
 *
 * `spec_jsonb` itself stays `Jsonb<unknown>` at the DB layer (same
 * reasoning as K8's `EventStoreRepository.type: string` staying
 * non-generic -- the infra layer doesn't get to assume the value in the
 * column matches this shape, since nothing at the DB write path
 * currently enforces `contracts/activity_spec.schema.json` outside of
 * `SpecLintService`, and only when a spec is actually published through
 * it). Real call sites read a `content.activity_version` row and cast
 * `spec_jsonb` to `ActivitySpec` (or a `Pick<>`/`Partial<>` of it) at the
 * point they consume it, same pattern as before, just against one real
 * shared type instead of a locally-invented guess.
 */

export type ActivityMode = 'GUIDED_LAB' | 'PRODUCTION_SIM' | 'PROJECT';

export type ActivityVersionStatusSpec =
  | 'DRAFT'
  | 'IN_REVIEW'
  | 'APPROVED'
  | 'PUBLISHED'
  | 'CANARY'
  | 'DEPRECATED'
  | 'RETIRED'
  | 'ROLLED_BACK';

export interface ActivitySpecMeta {
  title: string;
  summary: string;
  difficulty_level: 'L1' | 'L2' | 'L3' | 'L4' | 'L5';
  estimated_minutes: number;
  locale?: string;
  authors?: string[];
  tags?: string[];
}

export interface ActivitySpecCurriculum {
  primary_topic: string;
  also_relevant?: string[];
  courses?: string[];
}

export interface ActivitySpecSkill {
  skill: string;
  weight: number;
  primary: boolean;
  bloom?:
    'remember' | 'understand' | 'apply' | 'analyze' | 'evaluate' | 'create';
}

export interface ActivitySpecPrerequisites {
  hard?: string[];
  soft?: string[];
}

export interface ActivitySpecEnvironmentSeed {
  fixture: string;
}

export interface ActivitySpecEnvironment {
  tier: 'BROWSER' | 'SHARED_CONTAINER' | 'ISOLATED_VM' | 'CLOUD_ACCOUNT';
  blueprint: string;
  resources?: { cpu?: string; memory?: string; disk?: string };
  ttl_minutes?: number;
  idle_timeout_minutes?: number;
  network_policy?: string;
  seed?: ActivitySpecEnvironmentSeed[];
  cost_budget_usd: number;
}

export type ActivitySurface =
  'terminal' | 'editor' | 'k8s_dashboard_readonly' | 'preview';

export interface ActivitySpecHealthGateCheck {
  type: 'HTTP_PROBE' | 'K8S_ASSERT';
  url?: string;
  expect_status?: number;
  retries?: number;
}

export interface ActivitySpecFault {
  id: string;
  params?: Record<string, unknown>;
  apply_at: string; // 'T0' | `T+${number}` -- see contracts/activity_spec.schema.json's pattern
}

export interface ActivitySpecProcessSignals {
  diagnostic_efficiency?: {
    good_actions?: string[];
    bad_actions?: string[];
    scoring?: 'ratio_and_ordering';
  };
  blast_radius?: {
    forbidden?: string[];
  };
}

export interface ActivitySpecArtifactRequired {
  key: string;
  type: 'MARKDOWN';
  rubric?: string;
}

export interface ActivitySpecValidator {
  id: string;
  type:
    | 'SHELL_ASSERT'
    | 'SHELL_JSON'
    | 'FILE_EXISTS'
    | 'FILE_CONTENT'
    | 'FILE_PARSE'
    | 'K8S_ASSERT'
    | 'K8S_EVENT_ABSENT'
    | 'HTTP_PROBE'
    | 'HTTP_SLO'
    | 'CLOUD_ASSERT'
    | 'IAC_STATE'
    | 'DB_QUERY'
    | 'TEST_SUITE'
    | 'STATIC_ANALYSIS'
    | 'PERF_BENCH'
    | 'CHAOS_PROBE'
    | 'TELEMETRY_ASSERT'
    | 'NO_REGRESSION'
    | 'AI_RUBRIC';
  run?: string;
  expect: Record<string, unknown>;
  weight: number;
  severity?: 'BLOCKING' | 'WARN';
  on_fail: string;
  timeout_ms?: number;
  retry?: Record<string, unknown>;
}

export interface ActivitySpecHint {
  level: number;
  penalty: number;
  text: string;
}

export interface ActivitySpecTask {
  key: string;
  title: string;
  required: boolean;
  instructions_md: string;
  validators: ActivitySpecValidator[];
  hints?: ActivitySpecHint[];
  solution_apply?: string;
  telemetry_signals?: string[];
}

export interface ActivitySpecCompletion {
  rule: 'ALL_REQUIRED_TASKS_PASS';
  min_score?: number;
}

export interface ActivitySpecScoring {
  profile: string;
  overrides?: { weights?: Record<string, number> };
}

export interface ActivitySpecAiMentor {
  persona?: 'tutor' | 'senior_engineer' | 'reviewer';
  max_hints?: number;
  token_budget?: number;
  solution_visibility?: 'HIDDEN' | 'AFTER_SUBMIT' | 'ALWAYS';
  policy?: string;
}

export interface ActivitySpecReferenceSolution {
  repo_path?: string;
  visibility?: 'HIDDEN' | 'AFTER_PASS_OR_EXHAUST' | 'AFTER_SUBMIT';
}

export interface ActivitySpecLifecycle {
  resume_window_days?: number;
  max_attempts?: number | null;
  retire_after?: string | null;
}

/** Full mirror of contracts/activity_spec.schema.json. */
export interface ActivitySpec {
  id: string;
  version: number;
  mode: ActivityMode;
  status: ActivityVersionStatusSpec;
  meta: ActivitySpecMeta;
  curriculum: ActivitySpecCurriculum;
  skills: ActivitySpecSkill[];
  prerequisites?: ActivitySpecPrerequisites;
  objectives?: string[];
  environment: ActivitySpecEnvironment;
  surfaces?: ActivitySurface[];
  health_gate?: ActivitySpecHealthGateCheck[];
  faults?: ActivitySpecFault[];
  process_signals?: ActivitySpecProcessSignals;
  artifacts_required?: ActivitySpecArtifactRequired[];
  tasks: ActivitySpecTask[];
  completion: ActivitySpecCompletion;
  scoring: ActivitySpecScoring;
  ai_mentor?: ActivitySpecAiMentor;
  reference_solution?: ActivitySpecReferenceSolution;
  lifecycle?: ActivitySpecLifecycle;
}
