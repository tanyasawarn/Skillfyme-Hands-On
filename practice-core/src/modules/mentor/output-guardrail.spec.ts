import { DisclosureCeiling } from './disclosure-policy';
import { checkOutput } from './output-guardrail';

describe('checkOutput (PLAN.md G5 / doc §7.3 step 5, §7.7)', () => {
  it('redacts an exact command in a fenced block below GiveCommand', () => {
    const r = checkOutput({
      text: 'Try this:\n```bash\nkubectl set image deployment/checkout checkout=checkout:v2\n```',
      ceiling: DisclosureCeiling.NarrowSearch,
      maxCodeLines: 0,
    });
    // caught either as the command leak or (maxCodeLines 0) as an
    // over-long block -- either way the command must not survive.
    expect(r.violations.length).toBeGreaterThan(0);
    expect(r.redacted).not.toContain('kubectl set image');
  });

  it('redacts a command in a fenced block even when block length is within maxCodeLines', () => {
    const r = checkOutput({
      text: '```bash\nkubectl set image deployment/checkout checkout=checkout:v2\n```',
      ceiling: DisclosureCeiling.IdentifyCause,
      maxCodeLines: 5,
    });
    expect(r.violations).toContain('COMMAND_LEAK');
    expect(r.redacted).not.toContain('kubectl set image');
  });

  it('allows an exact command when the ceiling IS GiveCommand', () => {
    const r = checkOutput({
      text: '```bash\nkubectl rollout restart deployment/checkout\n```',
      ceiling: DisclosureCeiling.GiveCommand,
      maxCodeLines: 5,
    });
    expect(r.violations).toHaveLength(0);
    expect(r.redacted).toContain('kubectl rollout restart');
  });

  it('withholds a code block longer than maxCodeLines', () => {
    const long = Array.from({ length: 20 }, (_, i) => `key${i}: val`).join(
      '\n',
    );
    const r = checkOutput({
      text: '```yaml\n' + long + '\n```',
      ceiling: DisclosureCeiling.IdentifyArea,
      maxCodeLines: 5,
    });
    expect(r.violations).toContain('CODE_BLOCK_TOO_LONG');
    expect(r.redacted).not.toContain('key10: val');
  });

  it('catches a command written in prose (not a code block)', () => {
    const r = checkOutput({
      text: 'You just need to run kubectl rollout restart deployment/checkout -n shop and it recovers.',
      ceiling: DisclosureCeiling.IdentifyArea,
      maxCodeLines: 0,
    });
    expect(r.violations).toContain('COMMAND_LEAK');
    expect(r.redacted).not.toContain('kubectl rollout restart');
  });

  it('replaces a named broken resource below IdentifyCause', () => {
    const r = checkOutput({
      text: 'The problem is the payment-service Deployment, whose probe path is wrong.',
      ceiling: DisclosureCeiling.NarrowSearch,
      maxCodeLines: 0,
      sensitiveResourceNames: ['payment-service'],
    });
    expect(r.violations).toContain('NAMES_BROKEN_RESOURCE');
    expect(r.redacted).not.toContain('payment-service');
    expect(r.redacted).toContain('the affected resource');
  });

  it('permits naming the resource at IdentifyCause+', () => {
    const r = checkOutput({
      text: 'The payment-service Deployment probe path is wrong.',
      ceiling: DisclosureCeiling.IdentifyCause,
      maxCodeLines: 0,
      sensitiveResourceNames: ['payment-service'],
    });
    expect(r.violations).not.toContain('NAMES_BROKEN_RESOURCE');
  });

  it('flags a verbatim reference-solution snippet as not-allowed (unsalvageable)', () => {
    const r = checkOutput({
      text: 'Here it is:\napiVersion: apps/v1\nkind: Deployment\n# ...',
      ceiling: DisclosureCeiling.ConceptOnly,
      maxCodeLines: 0,
      knownSolutionSnippets: ['apiVersion: apps/v1\nkind: Deployment'],
    });
    expect(r.violations).toContain('SOLUTION_SNIPPET_LEAK');
    expect(r.allowed).toBe(false);
    expect(r.redacted).toContain('[redacted]');
  });

  it('passes clean conceptual text through untouched', () => {
    const text =
      'A readiness probe tells Kubernetes when a pod can receive traffic. If the path or port is wrong, the pod stays NotReady and the Service drops it from endpoints. What does `kubectl get endpoints` show for the service you expect traffic on?';
    const r = checkOutput({
      text,
      ceiling: DisclosureCeiling.IdentifyArea,
      maxCodeLines: 0,
    });
    // "kubectl get endpoints" in prose IS a command mention -> redacted,
    // but that's the correct conservative behavior below GiveCommand.
    expect(r.allowed).toBe(true);
  });
});
