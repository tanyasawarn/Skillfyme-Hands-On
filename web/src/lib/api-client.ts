/**
 * Typed client over practice-core's HTTP surface (doc §8.3, scoped to
 * what's actually implemented so far). One function per endpoint that
 * exists today -- do not add speculative endpoints here before
 * practice-core implements them; that produces silently-broken UI.
 *
 * Route note: practice-core uses `/attempts/{id}/submit` (slash), not the
 * doc's literal `/attempts/{id}:submit` (colon) -- see
 * practice-core memory notes: NestJS's router version rejects colon-action
 * routes at boot. This client matches what the server actually serves.
 */

import { getAuthToken } from './auth-token';
import { API_BASE_URL } from './config';

export interface CatalogEntry {
  activity_id: string;
  slug: string;
  mode: 'GUIDED_LAB' | 'PRODUCTION_SIM' | 'PROJECT';
  activity_version_id: string;
  version: number;
  difficulty_level: 'L1' | 'L2' | 'L3' | 'L4' | 'L5' | null;
  estimated_minutes: number | null;
  cost_budget_usd: string | null;
}

/** Doc §3.2 ActivitySpec shape, fields the UI actually renders. */
export interface ActivitySpec {
  meta?: { title?: string; summary?: string };
  objectives?: string[];
  tasks?: Array<{ key: string; title: string; required: boolean; instructions_md: string }>;
  /**
   * Content-authored per lab: which workspace panes this activity's
   * tasks actually need. 'editor' is present on labs that require
   * authoring/inspecting a file directly (K8s manifests, Terraform,
   * Dockerfiles, CI/CD configs, ...); terminal-only labs (git basics,
   * filesystem navigation, etc.) omit it. Every published spec sets
   * this explicitly -- see content/activities/*.yaml.
   */
  surfaces?: Array<'terminal' | 'editor'>;
}

export interface CatalogEntryDetail extends CatalogEntry {
  status: string;
  spec_jsonb: ActivitySpec;
}

/** Doc §2.1/§8.3: skill-driven catalog entry point -- one row per curriculum
 * skill, carrying the PUBLISHED activity that covers it (if any). A skill
 * with no matching activity comes back with every activity_* field null. */
export interface SkillCatalogEntry {
  skill_id: string;
  skill_slug: string;
  skill_name: string;
  skill_domain: string;
  activity_id: string | null;
  activity_slug: string | null;
  activity_mode: 'GUIDED_LAB' | 'PRODUCTION_SIM' | 'PROJECT' | null;
  activity_version_id: string | null;
  activity_version: number | null;
  difficulty_level: 'L1' | 'L2' | 'L3' | 'L4' | 'L5' | null;
  estimated_minutes: number | null;
  cost_budget_usd: string | null;
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

/** Doc §5.4: POST /v1/practice/attempts/{id}/connect -- mints a fresh terminal WS session. */
export interface ConnectInfo {
  terminalWsUrl: string;
  editorUrl: string;
  sessionToken: string;
  expiresAt: string;
}

export interface Attempt {
  id: string;
  tenant_id: string;
  user_id: string;
  activity_id: string;
  activity_version_id: string;
  mode: string;
  status: AttemptStatus;
  environment_id: string | null;
  created_at: string;
  started_at: string | null;
  submitted_at: string | null;
  completed_at: string | null;
  version: number;
}

/** Doc §8.3: GET /v1/practice/attempts/{id}/evaluation. */
export interface AttemptEvaluation {
  final_score: string;
  passed: boolean;
  profile_version_id: string;
  breakdown_jsonb: Record<string, { value: number; weight: number; explanation: Record<string, unknown> }>;
  penalties_jsonb: Record<string, number>;
  computed_at: string;
}

/** Doc §8.3: GET /v1/practice/attempts/{id}/tasks. */
export interface AttemptTaskState {
  task_key: string;
  status: 'PENDING' | 'PASSED' | 'FAILED' | 'SKIPPED';
  attempts_count: number;
  hints_used_max_level: number;
  skipped: boolean;
}

/**
 * POST /v1/practice/attempts/{id}/check -- learner-triggered validation
 * ("Check my work"). Runs the validator set without ending the attempt.
 */
export interface AttemptCheckResult {
  tasks: Array<{
    task_key: string;
    required: boolean;
    passed: boolean;
    validators: Array<{
      validator_id: string;
      status: 'PASS' | 'FAIL' | 'ERROR' | 'SKIP';
      severity: 'BLOCKING' | 'WARN';
    }>;
  }>;
  all_required_passed: boolean;
}

/** Doc §7.5 hint ladder, static-authored subset. */
export interface HintPreview {
  taskKey: string;
  nextLevel: number;
  penalty: number;
  hasMoreAfterThis: boolean;
}

export interface HintReveal extends HintPreview {
  text: string;
}

/** Doc §1.2 Skills nav: mastery per skill + band + evidence. */
export interface SkillMastery {
  skill_id: string;
  slug: string;
  name: string;
  domain: string;
  p_mastery: string;
  band: 'Novice' | 'Developing' | 'Competent' | 'Proficient' | 'Mastered';
  evidence_count: number;
  last_evidence_at: string | null;
}

export interface Recommendation {
  activityId: string;
  activityVersionId: string;
  slug: string;
  score: number;
  reasonCode: 'CURRICULUM_ADJACENT' | 'REMEDIATION';
  reasonParams: Record<string, unknown>;
}

/** Doc §1.2: Home is a decision surface -- Continue / Recommended / Mastery snapshot / Recent. */
export interface Dashboard {
  continueAttempt: Attempt | null;
  recommended: Recommendation[];
  masterySnapshot: SkillMastery[];
  recent: Attempt[];
}

/** Doc §8.5: GET /v1/practice/attempts/{id}/files?dir= -- Monaco file tree, one directory level per call. */
export interface WorkspaceFileEntry {
  path: string;
  type: 'file' | 'dir';
}

export interface CourseTree {
  course: { id: string; slug: string; title: string };
  modules: Array<{
    id: string;
    title: string;
    topics: Array<{
      id: string;
      title: string;
      subtopics: Array<{ id: string; title: string }>;
      skills: Array<{ skill_slug: string; skill_name: string; coverage_weight: string; bloom_level: string }>;
    }>;
  }>;
}

class ApiError extends Error {
  constructor(
    public status: number,
    public body: unknown,
  ) {
    super(`API error ${status}: ${JSON.stringify(body)}`);
  }
}

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const token = await getAuthToken();
  const res = await fetch(`${API_BASE_URL}${path}`, {
    ...init,
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Bearer ${token}`,
      ...(init?.headers ?? {}),
    },
    cache: 'no-store',
  });
  if (!res.ok) {
    let body: unknown;
    try {
      body = await res.json();
    } catch {
      body = await res.text();
    }
    throw new ApiError(res.status, body);
  }
  return res.json() as Promise<T>;
}

export const api = {
  listCatalog: (tenantId: string) =>
    request<CatalogEntry[]>(`/v1/practice/activities?tenant_id=${encodeURIComponent(tenantId)}`),

  listSkillCatalog: (tenantId: string, courseSlug: string) =>
    request<SkillCatalogEntry[]>(
      `/v1/practice/activities/by-skill?tenant_id=${encodeURIComponent(tenantId)}&course=${encodeURIComponent(courseSlug)}`,
    ),

  getActivityVersion: (activityVersionId: string) =>
    request<CatalogEntryDetail>(`/v1/practice/activities/${activityVersionId}`),

  getAttempt: (id: string) => request<Attempt>(`/v1/practice/attempts/${id}`),

  listAttempts: (userId: string) =>
    request<Attempt[]>(`/v1/practice/attempts?user_id=${encodeURIComponent(userId)}`),

  createAttempt: (input: { tenant_id: string; user_id: string; activity_version_id: string }, idempotencyKey: string) =>
    request<Attempt>('/v1/practice/attempts', {
      method: 'POST',
      headers: { 'Idempotency-Key': idempotencyKey },
      body: JSON.stringify(input),
    }),

  provisionAttempt: (id: string) => request<Attempt>(`/v1/practice/attempts/${id}/provision`, { method: 'POST' }),
  startAttempt: (id: string) => request<Attempt>(`/v1/practice/attempts/${id}/start`, { method: 'POST' }),
  checkAttempt: (id: string) =>
    request<AttemptCheckResult>(`/v1/practice/attempts/${id}/check`, { method: 'POST' }),
  submitAttempt: (id: string) => request<Attempt>(`/v1/practice/attempts/${id}/submit`, { method: 'POST' }),
  connectAttempt: (id: string) => request<ConnectInfo>(`/v1/practice/attempts/${id}/connect`, { method: 'POST' }),

  getEvaluation: (id: string) => request<AttemptEvaluation | null>(`/v1/practice/attempts/${id}/evaluation`),
  getTasks: (id: string) => request<AttemptTaskState[]>(`/v1/practice/attempts/${id}/tasks`),

  previewHint: (attemptId: string, taskKey: string) =>
    request<HintPreview | null>(`/v1/practice/attempts/${attemptId}/tasks/${taskKey}/hints`),
  revealHint: (attemptId: string, taskKey: string) =>
    request<HintReveal>(`/v1/practice/attempts/${attemptId}/tasks/${taskKey}/hints`, { method: 'POST' }),

  getDashboard: (userId: string, tenantId: string) =>
    request<Dashboard>(`/v1/practice/dashboard?user_id=${encodeURIComponent(userId)}&tenant_id=${encodeURIComponent(tenantId)}`),
  getSkills: (userId: string, courseSlug: string) =>
    request<SkillMastery[]>(
      `/v1/practice/skills?user_id=${encodeURIComponent(userId)}&course=${encodeURIComponent(courseSlug)}`,
    ),
  getCourseTree: (slug: string, tenantId: string) =>
    request<CourseTree>(`/v1/practice/courses/${slug}?tenant_id=${encodeURIComponent(tenantId)}`),

  listWorkspaceFiles: (attemptId: string, dir: string) =>
    request<WorkspaceFileEntry[]>(`/v1/practice/attempts/${attemptId}/files?dir=${encodeURIComponent(dir)}`),
  readWorkspaceFile: (attemptId: string, path: string) =>
    request<{ content: string }>(`/v1/practice/attempts/${attemptId}/files/content?path=${encodeURIComponent(path)}`),
  writeWorkspaceFile: (attemptId: string, path: string, content: string) =>
    request<{ ok: boolean }>(`/v1/practice/attempts/${attemptId}/files/content?path=${encodeURIComponent(path)}`, {
      method: 'POST',
      body: JSON.stringify({ content }),
    }),
};

export { ApiError };
