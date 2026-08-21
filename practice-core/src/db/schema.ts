import type { ColumnType, Generated } from 'kysely';

// Kysely Database interface mirroring db/migrations/*.sql (doc §8.4).
// Table keys are "schema.table" to match Postgres schema-per-bounded-context.
// Kept hand-written (not codegen'd) deliberately: the doc's schema is fully
// specified up front, so this is a direct transcription, and hand-written
// keeps the doc's field names/types visibly traceable per column.

type Timestamp = ColumnType<Date, Date | string, Date | string>;
type Jsonb<T> = ColumnType<T, T, T>;

export interface TenantTable {
  id: Generated<string>;
  name: string;
  plan: Generated<string>;
  settings: Generated<Jsonb<Record<string, unknown>>>;
  created_at: Generated<Timestamp>;
}

export interface UserAccountTable {
  id: Generated<string>;
  tenant_id: string;
  email: string;
  role: Generated<string>;
  status: Generated<string>;
  created_at: Generated<Timestamp>;
}

export interface CourseTable {
  id: Generated<string>;
  tenant_id: string;
  slug: string;
  title: string;
  status: Generated<string>;
}

export interface ModuleTable {
  id: Generated<string>;
  course_id: string;
  title: string;
  position: number;
  slug: string | null;
}

export interface TopicTable {
  id: Generated<string>;
  module_id: string;
  title: string;
  position: number;
  slug: string | null;
}

export interface SubtopicTable {
  id: Generated<string>;
  topic_id: string;
  title: string;
  position: number;
}

export interface SkillTable {
  id: Generated<string>;
  slug: string;
  name: string;
  domain: string;
  description: string | null;
  decay_half_life_days: Generated<number>;
  bkt_p_init: Generated<number>;
  bkt_p_transit: Generated<number>;
  bkt_p_slip: Generated<number>;
  bkt_p_guess: Generated<number>;
  status: Generated<string>;
  domain_order: Generated<number>;
  sequence: Generated<number>;
  course_slug: Generated<string>;
}

export type SkillEdgeType =
  'REQUIRES' | 'BUILDS_ON' | 'SIBLING' | 'SPECIALIZES' | 'SUPERSEDES';

export interface SkillEdgeTable {
  from_skill_id: string;
  to_skill_id: string;
  type: SkillEdgeType;
  strength: Generated<number>;
}

export interface SkillClosureTable {
  ancestor_id: string;
  descendant_id: string;
  depth: number;
  edge_types: SkillEdgeType[];
}

export interface TopicSkillTable {
  topic_id: string;
  skill_id: string;
  coverage_weight: number;
  bloom_level: string;
}

export type ActivityMode = 'GUIDED_LAB' | 'PRODUCTION_SIM' | 'PROJECT';

export interface ActivityTable {
  id: Generated<string>;
  tenant_id: string;
  slug: string;
  mode: ActivityMode;
  owner_id: string | null;
  status: Generated<string>;
  retired_at: Timestamp | null;
}

export type ActivityVersionStatus =
  | 'DRAFT'
  | 'IN_REVIEW'
  | 'APPROVED'
  | 'PUBLISHED'
  | 'CANARY'
  | 'DEPRECATED'
  | 'RETIRED'
  | 'ROLLED_BACK';

export interface ActivityVersionTable {
  id: Generated<string>;
  activity_id: string;
  version: number;
  semver_kind: 'PATCH' | 'MINOR' | 'MAJOR' | null;
  status: Generated<ActivityVersionStatus>;
  spec_jsonb: Jsonb<unknown>;
  blueprint_id: string | null;
  blueprint_version: string | null;
  scoring_profile_id: string | null;
  scoring_profile_version: string | null;
  difficulty_level: 'L1' | 'L2' | 'L3' | 'L4' | 'L5' | null;
  difficulty_elo: number | null;
  estimated_minutes: number | null;
  cost_budget_usd: number | null;
  canary_pct: Generated<number>;
  published_at: Timestamp | null;
  published_by: string | null;
  created_at: Generated<Timestamp>;
}

export interface ActivitySkillTable {
  activity_version_id: string;
  skill_id: string;
  weight: number;
  is_primary: Generated<boolean>;
  bloom_level: string | null;
}

export interface ActivityTopicTable {
  activity_version_id: string;
  topic_id: string;
  relevance: Generated<number>;
}

export type AttemptStatus =
  | 'CREATED'
  | 'PROVISIONING'
  | 'READY'
  | 'IN_PROGRESS'
  | 'SUBMITTED'
  | 'EVALUATING'
  | 'PASSED'
  | 'FAILED'
  | 'COMPLETED'
  | 'PROVISION_FAILED'
  | 'SUSPENDED'
  | 'EVAL_FAILED'
  | 'EXPIRED'
  | 'ABANDONED';

export interface AttemptTable {
  id: Generated<string>;
  tenant_id: string;
  user_id: string;
  activity_id: string;
  activity_version_id: string;
  mode: string;
  status: Generated<AttemptStatus>;
  retry_of_attempt_id: string | null;
  retry_index: Generated<number>;
  assistance_flags: Generated<string[]>;
  environment_id: string | null;
  idempotency_key: string | null;
  created_at: Generated<Timestamp>;
  started_at: Timestamp | null;
  submitted_at: Timestamp | null;
  completed_at: Timestamp | null;
  expires_at: Timestamp | null;
  active_seconds: Generated<number>;
  reset_count: Generated<number>;
  hint_penalty_total: Generated<number>;
  version: Generated<number>;
}

export interface AttemptTaskStateTable {
  attempt_id: string;
  task_key: string;
  status: Generated<string>;
  first_pass_at: Timestamp | null;
  attempts_count: Generated<number>;
  hints_used_max_level: Generated<number>;
  skipped: Generated<boolean>;
  assisted: Generated<boolean>;
}

export type EventActor = 'LEARNER' | 'SYSTEM' | 'VALIDATOR' | 'AI' | 'ADMIN';

export interface AttemptEventsTable {
  id: Generated<string>;
  attempt_id: string;
  seq: string; // bigint arrives as string over the pg driver by default
  occurred_at: Timestamp;
  actor: EventActor;
  type: string;
  payload: Jsonb<unknown>;
}

export interface ValidationRunTable {
  id: Generated<string>;
  attempt_id: string;
  scope: string;
  trigger: string;
  started_at: Generated<Timestamp>;
  finished_at: Timestamp | null;
  status: Generated<string>;
}

export type ValidatorResultStatus = 'PASS' | 'FAIL' | 'ERROR' | 'SKIP';

export interface ValidatorResultTable {
  id: Generated<string>;
  validation_run_id: string;
  validator_id: string;
  status: ValidatorResultStatus;
  observed: Jsonb<unknown> | null;
  expected: Jsonb<unknown> | null;
  duration_ms: number | null;
  evidence_ref: string | null;
}

export interface AttemptSignalTable {
  attempt_id: string;
  signal_key: string;
  value_num: number | null;
  value_jsonb: Jsonb<unknown> | null;
  computed_at: Generated<Timestamp>;
}

export interface AttemptScoreTable {
  id: Generated<string>;
  attempt_id: string;
  profile_version_id: string;
  criterion_fn_versions_jsonb: Jsonb<Record<string, string>>;
  final_score: number;
  passed: boolean;
  breakdown_jsonb: Jsonb<Record<string, unknown>>;
  penalties_jsonb: Generated<Jsonb<Record<string, unknown>>>;
  computed_at: Generated<Timestamp>;
  rescored_from_id: string | null;
  rescore_reason: string | null;
}

export interface ArtifactTable {
  id: Generated<string>;
  attempt_id: string;
  kind: string;
  storage_uri: string;
  bytes: number | null;
  checksum: string | null;
  created_at: Generated<Timestamp>;
  retain_until: Timestamp | null;
}

export interface SkillMasteryTable {
  user_id: string;
  skill_id: string;
  p_mastery: number;
  last_evidence_at: Timestamp | null;
  evidence_count: Generated<number>;
  elo_rating: number | null;
  review_due_at: Timestamp | null;
  band: string | null;
}

export interface MasteryEvidenceTable {
  id: Generated<string>;
  user_id: string;
  skill_id: string;
  attempt_id: string;
  delta: number;
  p_before: number;
  p_after: number;
  weight: number;
  created_at: Generated<Timestamp>;
}

export interface LearnerActivityStateTable {
  user_id: string;
  activity_id: string;
  best_score: number | null;
  latest_score: number | null;
  status: Generated<string>;
  attempts_count: Generated<number>;
  last_attempt_at: Timestamp | null;
  cooldown_until: Timestamp | null;
}

export interface LearnerEloTable {
  user_id: string;
  skill_domain: string;
  rating: Generated<number>;
  matches: Generated<number>;
}

export interface RecommendationTable {
  id: Generated<string>;
  user_id: string;
  activity_id: string;
  score: number;
  features_jsonb: Generated<Jsonb<Record<string, unknown>>>;
  reason_code: string;
  reason_params_jsonb: Generated<Jsonb<Record<string, unknown>>>;
  generated_at: Generated<Timestamp>;
  shown_at: Timestamp | null;
  clicked_at: Timestamp | null;
  started_at: Timestamp | null;
  dismissed_at: Timestamp | null;
  ranker_version: Generated<string>;
}

export interface Database {
  'learner.tenant': TenantTable;
  'learner.user_account': UserAccountTable;
  'learner.learner_activity_state': LearnerActivityStateTable;
  'learner.learner_elo': LearnerEloTable;
  'learner.recommendation': RecommendationTable;

  'content.course': CourseTable;
  'content.module': ModuleTable;
  'content.topic': TopicTable;
  'content.subtopic': SubtopicTable;
  'content.topic_skill': TopicSkillTable;
  'content.activity': ActivityTable;
  'content.activity_version': ActivityVersionTable;
  'content.activity_skill': ActivitySkillTable;
  'content.activity_topic': ActivityTopicTable;

  'skill.skill': SkillTable;
  'skill.skill_edge': SkillEdgeTable;
  'skill.skill_closure': SkillClosureTable;
  'skill.skill_mastery': SkillMasteryTable;
  'skill.mastery_evidence': MasteryEvidenceTable;

  'attempt.attempt': AttemptTable;
  'attempt.attempt_task_state': AttemptTaskStateTable;
  'attempt.attempt_events': AttemptEventsTable;
  'attempt.validation_run': ValidationRunTable;
  'attempt.validator_result': ValidatorResultTable;
  'attempt.attempt_signal': AttemptSignalTable;
  'attempt.attempt_score': AttemptScoreTable;
  'attempt.artifact': ArtifactTable;
}
