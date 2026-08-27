import type { EventActor } from '../../db/schema';
import type { EventStoreRepository } from './event-store.repository';

/**
 * PLAN.md Phase 3's K8: `AttemptEventType`, replacing 20+ event-type
 * string literals scattered across 7 files with zero shared taxonomy
 * (AppendEventInput.type/AttemptEventRow.type were both a bare `string`,
 * confirmed no compile-time check existed before this -- a typo in an
 * event type string would silently write/read the wrong bucket with no
 * error anywhere).
 *
 * Mirrors contracts/events.md's own Taxonomy table exactly (same 29
 * types, same categories as that doc's section headers) -- this is the
 * canonical, cross-service contract Dev A (Go orchestrator) and Dev B
 * (this service) both publish/consume against, so the union here must
 * stay a strict match to that doc, not just "whatever this codebase
 * happens to use today." Several of these (SUSPENDED, RESUMED, SEALED,
 * FILE_CHANGED, TERMINAL_SESSION_OPEN/CLOSE, TASK_SKIPPED,
 * SOLUTION_VIEWED, RESET, IDLE_DETECTED, TTL_WARNING, ENV_DESTROYED,
 * SNAPSHOT_TAKEN, TICKET_OPENED, ESCALATION_FIRED, MILESTONE_SUBMITTED)
 * are real contract entries with no current writer/reader in this
 * codebase yet (Phase 2/3/4 scope per the doc's own Notes column) --
 * included so a future implementation of one of those doesn't need to
 * touch this file, and so ReplayService's own switch (which already has
 * to handle "an event type it doesn't specifically branch on" as a
 * no-op default) stays exhaustively checkable against the real contract
 * shape.
 */
export const ATTEMPT_EVENT_TYPES = [
  // Lifecycle
  'ATTEMPT_CREATED',
  'ENV_REQUESTED',
  'ENV_READY',
  'ENV_FAILED',
  'ATTEMPT_STARTED',
  'SUSPENDED',
  'RESUMED',
  'SUBMITTED',
  'EVALUATED',
  'SEALED',
  // Execution
  'COMMAND_EXECUTED',
  'FILE_CHANGED',
  'EDITOR_SAVE',
  'TERMINAL_SESSION_OPEN',
  'TERMINAL_SESSION_CLOSE',
  // Validation
  'VALIDATION_REQUESTED',
  'VALIDATOR_RESULT',
  'TASK_PASSED',
  'TASK_FAILED',
  'TASK_SKIPPED',
  // Assistance
  'HINT_REQUESTED',
  'AI_MESSAGE',
  'SOLUTION_VIEWED',
  // Environment
  'RESET',
  'FAULT_INJECTED',
  'IDLE_DETECTED',
  'TTL_WARNING',
  'ENV_DESTROYED',
  'SNAPSHOT_TAKEN',
  // Scenario
  'TICKET_OPENED',
  'ESCALATION_FIRED',
  'MILESTONE_SUBMITTED',
] as const;

export type AttemptEventType = (typeof ATTEMPT_EVENT_TYPES)[number];

/**
 * Strictly-typed input for a real business event -- `type` here is
 * AttemptEventType directly (not generic, not defaulted), so a typo
 * like 'ATTEMPT_CREATD' is a real compile error. See
 * event-store.repository.ts's own doc comment for why this couldn't be
 * a generic type parameter on AppendEventInput itself (TypeScript
 * generic defaults don't constrain successfully-inferred literals, so a
 * defaulted-but-not-constrained parameter silently accepted typos in a
 * live test before this fix; a constrained parameter broke the
 * repository's own generic-infra-layer tests, which need to pass
 * arbitrary non-taxonomy strings).
 */
export interface TypedAppendEventInput {
  attemptId: string;
  actor: EventActor;
  type: AttemptEventType;
  payload: unknown;
  occurredAt?: Date;
}

/**
 * Thin, strictly-typed wrapper over EventStoreRepository.append() --
 * the actual K8 fix real call sites use. Takes the repository instance
 * rather than being a method ON EventStoreRepository so the repository
 * itself can stay a plain, generic `type: string` log (see that file's
 * own doc comment) while every real business-event call site still gets
 * genuine compile-time taxonomy checking via this function's parameter
 * type.
 */
export function appendTypedEvent(
  events: EventStoreRepository,
  input: TypedAppendEventInput,
): Promise<{ seq: string }> {
  return events.append(input);
}
