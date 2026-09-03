import type { ActivityMode } from '../../db/schema';

/**
 * PLAN.md G5 / doc §7.3 -- persona + disclosure-ceiling policy per mode.
 *
 * The disclosure ceiling is a 0-4 numeric parameter resolved per message:
 *   0 concept_only    -- explain the concept, nothing situation-specific
 *   1 narrow_search   -- point at a general area to look
 *   2 identify_area   -- name the subsystem / task that's wrong
 *   3 identify_cause  -- state the specific broken resource / root cause
 *   4 give_command    -- give the exact command / code
 *
 * It is resolved from: mode (the §7.3 table's hard ceilings) x the
 * learner's mastery on this activity's skills (lower mastery -> allow a
 * touch more) x time_stuck x an activity-level override, then CLAMPED to
 * the mode ceiling. It is passed to OutputGuardrailService, which checks
 * the *generated text* against it (prompt instructions alone do not hold
 * the line -- doc §7.3).
 */

export enum DisclosureCeiling {
  ConceptOnly = 0,
  NarrowSearch = 1,
  IdentifyArea = 2,
  IdentifyCause = 3,
  GiveCommand = 4,
}

export type Persona = 'PATIENT_TUTOR' | 'SENIOR_ONCALL' | 'STAFF_REVIEWER';

export interface DisclosureResolution {
  persona: Persona;
  ceiling: DisclosureCeiling;
  /** true iff the fault's canonical_diagnostic_path may be put in context
   *  (doc §7.4: "passed only when disclosure_ceiling >= 2"). */
  mayUseCanonicalPath: boolean;
  /** max lines of code/manifest the mentor may emit (doc §7.3 row). */
  maxCodeLines: number;
}

// Hard per-mode ceilings from the §7.3 table.
//  - GUIDED_LAB: "may give exact commands only at hint level 3" -> up to
//    GiveCommand, but only once the hint ladder is deep (caller passes
//    hintLevelReached). Base ceiling IdentifyCause; GiveCommand unlocked
//    at hintLevelReached >= 3.
//  - PRODUCTION_SIM: "Never name the specific broken resource",
//    "Never give exact commands" -> ceiling NarrowSearch.
//  - PROJECT: reviewer; may review architecture fully, but never the
//    learner's solution / commands -> ceiling IdentifyArea (can discuss
//    the area/pattern, not hand over a fix).
const MODE_MAX: Record<ActivityMode, DisclosureCeiling> = {
  GUIDED_LAB: DisclosureCeiling.GiveCommand,
  PRODUCTION_SIM: DisclosureCeiling.NarrowSearch,
  PROJECT: DisclosureCeiling.IdentifyArea,
};

const MODE_PERSONA: Record<ActivityMode, Persona> = {
  GUIDED_LAB: 'PATIENT_TUTOR',
  PRODUCTION_SIM: 'SENIOR_ONCALL',
  PROJECT: 'STAFF_REVIEWER',
};

const MODE_MAX_CODE_LINES: Record<ActivityMode, number> = {
  GUIDED_LAB: 5, // "Snippets <=5 lines, only illustrating syntax"
  PRODUCTION_SIM: 0, // "No"
  PROJECT: 0, // may show a *pattern*, not code -- handled as 0 emitted lines
};

export interface ResolveInput {
  mode: ActivityMode;
  /** mean P(mastery) across this activity's mapped skills, 0..1. */
  meanMastery: number;
  /** minutes since the learner last made progress on the current task. */
  timeStuckMinutes: number;
  /** deepest hint level the learner has already revealed on the current
   *  task (GUIDED_LAB only -- gates GiveCommand). */
  hintLevelReached?: number;
  /** activity spec's ai_mentor.disclosure_ceiling override, if authored. */
  activityOverride?: DisclosureCeiling;
}

export function resolveDisclosure(input: ResolveInput): DisclosureResolution {
  const modeMax = MODE_MAX[input.mode];

  // Base by mode.
  let ceiling: DisclosureCeiling =
    input.mode === 'GUIDED_LAB'
      ? DisclosureCeiling.IdentifyArea
      : input.mode === 'PRODUCTION_SIM'
        ? DisclosureCeiling.NarrowSearch
        : DisclosureCeiling.ConceptOnly;

  // Low mastery -> +1 (learner genuinely needs more scaffolding).
  if (input.meanMastery < 0.4) ceiling = ceiling + 1;

  // Stuck a long time -> +1.
  if (input.timeStuckMinutes >= 10) ceiling = ceiling + 1;

  // GUIDED_LAB: GiveCommand only after hint level 3.
  if (input.mode === 'GUIDED_LAB' && (input.hintLevelReached ?? 0) >= 3) {
    ceiling = DisclosureCeiling.GiveCommand;
  }

  // Activity override can only LOWER the ceiling, never raise it above
  // what mode+signals allow (author restricting further is fine; author
  // loosening past the mode ceiling is not).
  if (input.activityOverride != null) {
    ceiling = Math.min(ceiling, input.activityOverride);
  }

  // Clamp to the mode's hard ceiling and to [0,4].
  ceiling = Math.max(
    0,
    Math.min(ceiling, modeMax, DisclosureCeiling.GiveCommand),
  );

  return {
    persona: MODE_PERSONA[input.mode],
    ceiling,
    mayUseCanonicalPath: ceiling >= DisclosureCeiling.IdentifyArea,
    maxCodeLines:
      ceiling >= DisclosureCeiling.GiveCommand
        ? MODE_MAX_CODE_LINES[input.mode]
        : Math.min(
            MODE_MAX_CODE_LINES[input.mode],
            input.mode === 'GUIDED_LAB' ? 5 : 0,
          ),
  };
}
