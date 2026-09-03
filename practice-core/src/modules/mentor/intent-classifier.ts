/**
 * PLAN.md G4 / doc §7.2 step 2 ("INTENT CLASSIFY: concept_q | error_help
 * | 'just tell me' | off_topic | injection").
 *
 * A cheap lexical pre-classifier. A real deployment routes ambiguous
 * cases to the fast model (LlmGateway taskClass 'intent_classify'); this
 * heuristic handles the clear majority and, crucially, flags injection
 * attempts (which are logged as an integrity signal, doc rule 35).
 */

export type Intent =
  'concept_q' | 'error_help' | 'just_tell_me' | 'off_topic' | 'injection';

const INJECTION_MARKERS = [
  /ignore (?:all |the )?(?:previous|above|prior) (?:instructions|rules)/i,
  /you are now\b/i,
  /disregard (?:your|the) (?:system prompt|rules|instructions)/i,
  /\bdisclosure[_ ]?ceiling\s*(?:=|:|\bis\b)\s*[2-4]/i,
  /print (?:your|the) (?:system prompt|instructions|solution|reference)/i,
  /\bDAN\b|\bdev ?mode\b|jailbreak/i,
  /roleplay .* (?:solution|answer key|reference)/i,
  /decode this and (?:follow|do)/i,
];

const JUST_TELL_ME = [
  /just (?:tell|give) me (?:the )?(?:answer|solution|command|fix)/i,
  /what(?:'s| is) the (?:exact )?command\b/i,
  /(?:give|show) me the (?:full|complete|corrected) (?:manifest|yaml|code|file)/i,
  /can you (?:just )?fix it (?:for me)?/i,
];

const ERROR_HELP = [
  /error|failed|failing|not working|crashloop|exit code|stderr|traceback|exception|denied|refused|timeout|timing out|unhealthy|not ?ready|502|503|500\b/i,
  /\bwhy\b/i, // "...timing out, why?" / "why is this happening"
  /what does this (?:mean|error)/i,
];

const OFF_TOPIC = [
  /\b(weather|sports|movie|recipe|stock price|joke|poem)\b/i,
  /who (?:are you|made you|built you)\b/i,
];

export function classifyIntent(text: string): Intent {
  const t = text.trim();
  if (INJECTION_MARKERS.some((re) => re.test(t))) return 'injection';
  if (JUST_TELL_ME.some((re) => re.test(t))) return 'just_tell_me';
  if (OFF_TOPIC.some((re) => re.test(t))) return 'off_topic';
  if (ERROR_HELP.some((re) => re.test(t))) return 'error_help';
  return 'concept_q';
}
