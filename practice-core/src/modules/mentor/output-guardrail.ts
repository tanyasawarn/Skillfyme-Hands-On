import { DisclosureCeiling } from './disclosure-policy';

/**
 * PLAN.md G5 / doc §7.3 step 5 ("OUTPUT GUARDRAIL: disclosure check,
 * safety, command-leak detector") and §7.7. Checks the mentor's
 * GENERATED text against the resolved disclosure ceiling -- because a
 * prompt instruction alone will not reliably hold the line.
 *
 * Returns { allowed, violations, redacted } -- the Mentor Service either
 * sends `redacted` (guardrail scrubbed it into bounds) or, if a
 * violation cannot be redacted safely, refuses and asks the learner a
 * question instead.
 */

export interface GuardrailInput {
  text: string;
  ceiling: DisclosureCeiling;
  maxCodeLines: number;
  /** Resource identifiers from the env-state summary that name the
   *  specific broken thing -- if the ceiling is below IdentifyCause the
   *  mentor must not state these. */
  sensitiveResourceNames?: string[];
  /** The activity's reference-solution / solution_apply text, if the
   *  caller has it (it MUST NOT -- G3's IAM boundary). Passed here only
   *  so a test can assert the leak detector fires; production callers
   *  leave it undefined. */
  knownSolutionSnippets?: string[];
}

export interface GuardrailResult {
  allowed: boolean;
  violations: GuardrailViolation[];
  /** text with over-ceiling content redacted; empty if not salvageable. */
  redacted: string;
}

export type GuardrailViolation =
  | 'COMMAND_LEAK' // exact shell command below GiveCommand ceiling
  | 'CODE_BLOCK_TOO_LONG' // fenced code exceeds maxCodeLines
  | 'NAMES_BROKEN_RESOURCE' // states a specific resource below IdentifyCause
  | 'SOLUTION_SNIPPET_LEAK'; // verbatim reference-solution text

const TOOLS =
  'kubectl|docker|helm|terraform|tofu|aws|gcloud|az|git|npm|yarn|pnpm|cargo|go|make|systemctl|journalctl|curl|wget|bash|sh|python3?|pip3?|psql|redis-cli|nc|ssh|scp|chmod|chown|mkdir|rm|mv|cp|tar|sed|awk';

// A command LINE: a whole line that is a runnable shell invocation.
const COMMAND_LINE = new RegExp(
  `^\\s*(?:\\$\\s*)?(?:sudo\\s+)?(?:${TOOLS})\\b`,
);

// A command mentioned in PROSE: "...run `kubectl ...`" / "just run kubectl
// rollout restart ...". Requires the tool token to be followed by a
// plausible subcommand/flag so a bare noun ("the kubectl docs") doesn't
// trip it.
const COMMAND_IN_PROSE = new RegExp(
  `\\b(?:${TOOLS})\\s+[a-z][\\w:-]+(?:\\s+[\\w./:=@-]+){1,}`,
);

const CODE_FENCE = /```[\s\S]*?```/g;

export function checkOutput(input: GuardrailInput): GuardrailResult {
  const violations: GuardrailViolation[] = [];
  let text = input.text;

  // --- 1. code fences: length + command-leak ------------------------
  text = text.replace(CODE_FENCE, (block) => {
    const inner = block.replace(/```[a-zA-Z0-9]*\n?/, '').replace(/```$/, '');
    const lines = inner.split('\n').filter((l) => l.trim().length > 0);

    if (lines.length > input.maxCodeLines) {
      violations.push('CODE_BLOCK_TOO_LONG');
      return '`[code example withheld — beyond what I can share for this activity]`';
    }
    if (
      input.ceiling < DisclosureCeiling.GiveCommand &&
      lines.some((l) => COMMAND_LINE.test(l))
    ) {
      violations.push('COMMAND_LEAK');
      return '`[exact command withheld — try to work out the invocation from the concept]`';
    }
    return block;
  });

  // --- 2. commands outside code fences: whole lines AND prose ------
  if (input.ceiling < DisclosureCeiling.GiveCommand) {
    const outLines = text.split('\n').map((line) => {
      if (COMMAND_LINE.test(line) && line.trim().split(/\s+/).length >= 2) {
        violations.push('COMMAND_LEAK');
        return '[exact command withheld]';
      }
      let out = line;
      // strip a command embedded mid-sentence (keep the surrounding prose)
      out = out.replace(COMMAND_IN_PROSE, (cmd) => {
        // ignore trivial 2-token mentions like "kubectl docs"
        if (cmd.trim().split(/\s+/).length < 3) return cmd;
        violations.push('COMMAND_LEAK');
        return 'that command';
      });
      // also strip inline-backtick commands: `kubectl get pods -n x`
      out = out.replace(/`([^`]+)`/g, (whole, inner: string) => {
        if (COMMAND_LINE.test(inner) && inner.trim().split(/\s+/).length >= 2) {
          violations.push('COMMAND_LEAK');
          return '`[command withheld]`';
        }
        return whole;
      });
      return out;
    });
    text = outLines.join('\n');
  }

  // --- 3. naming the specific broken resource ---------------------
  if (
    input.ceiling < DisclosureCeiling.IdentifyCause &&
    input.sensitiveResourceNames?.length
  ) {
    for (const name of input.sensitiveResourceNames) {
      if (!name || name.length < 3) continue;
      const re = new RegExp(escapeRegExp(name), 'g');
      if (re.test(text)) {
        violations.push('NAMES_BROKEN_RESOURCE');
        text = text.replace(re, 'the affected resource');
      }
    }
  }

  // --- 4. verbatim reference-solution leak ----------------------
  if (input.knownSolutionSnippets?.length) {
    for (const snip of input.knownSolutionSnippets) {
      if (snip && snip.length >= 12 && text.includes(snip)) {
        violations.push('SOLUTION_SNIPPET_LEAK');
        text = text.split(snip).join('[redacted]');
      }
    }
  }

  const unique = Array.from(new Set(violations));
  // A solution-snippet leak is never "salvageable by redaction" for
  // trust purposes -- flag the response as not allowed so the Mentor
  // Service regenerates or falls back to a question.
  const allowed = !unique.includes('SOLUTION_SNIPPET_LEAK');

  return { allowed, violations: unique, redacted: text };
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
