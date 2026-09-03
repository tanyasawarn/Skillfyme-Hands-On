import type { ValidatorExecutionResult } from '../validator-executor.interface';
import type { ShellRunner } from './shell-runner';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 3.7 / B3, IP-B3b). CHAOS_PROBE
 * validator: injects a fault into the learner's deployed system (kill a
 * pod, drain a node, delete a deployment replica, sever a dependency)
 * and asserts the service stays healthy through it (an HTTP probe stays
 * green for the recovery window).
 *
 * Config (mirrors ActivitySpecValidatorChaosProbeConfig):
 *   action:            'kill_pod' | 'drain_node' | 'cordon_node' |
 *                      'delete_deployment_replica' | 'sever_dependency'
 *   target_selector?:  kubectl-style selector for the action's target
 *   health_check:      { url, expect_status?, interval_ms? } — polled
 *                      throughout the recovery window
 *   recovery_timeout_ms?: how long the probe must stay green (default 60000)
 *
 * The chaos action itself runs via the ShellRunner using `kubectl` in
 * the workspace (D-P3-3: reuse the pod-kill / node-drain mechanism —
 * here through the workspace's own kubeconfig rather than a new
 * orchestrator RPC). A failure to *apply* the chaos action → ERROR. The
 * service going unhealthy during/after → FAIL.
 */
export interface ChaosProbeConfig {
  action:
    | 'kill_pod'
    | 'drain_node'
    | 'cordon_node'
    | 'delete_deployment_replica'
    | 'sever_dependency';
  target_selector?: string;
  health_check: { url: string; expect_status?: number; interval_ms?: number };
  recovery_timeout_ms?: number;
}

export async function runChaosProbe(
  runner: ShellRunner,
  validatorId: string,
  rawConfig: unknown,
): Promise<ValidatorExecutionResult> {
  const start = Date.now();
  const cfg = (rawConfig ?? {}) as ChaosProbeConfig;
  const err = (detail: string): ValidatorExecutionResult => ({
    validatorId,
    status: 'ERROR',
    durationMs: Date.now() - start,
    evidenceRef: detail,
  });

  if (!cfg.action) return err('CHAOS_PROBE has no `action` configured');
  if (!cfg.health_check?.url) {
    return err('CHAOS_PROBE needs a health_check.url to assert against');
  }
  const expectStatus = cfg.health_check.expect_status ?? 200;
  const intervalMs = cfg.health_check.interval_ms ?? 2000;
  const recoveryMs = cfg.recovery_timeout_ms ?? 60_000;
  const sel = cfg.target_selector ?? '';

  // --- pre-check: the service must be green BEFORE we break anything.
  const pre = await probeOnce(runner, cfg.health_check.url, expectStatus);
  if (pre === 'infra') return err('health check could not run (curl missing?)');
  if (pre === 'bad') {
    return err(
      'service was not healthy before the chaos action — nothing to probe',
    );
  }

  // --- apply the chaos action.
  const chaos = await applyChaos(runner, cfg.action, sel);
  if (chaos.infra) return err(`chaos action could not run: ${chaos.detail}`);
  if (!chaos.applied) {
    return err(`chaos action "${cfg.action}" did not apply: ${chaos.detail}`);
  }

  // --- probe continuously for the recovery window.
  const deadline = Date.now() + recoveryMs;
  const results: Array<'ok' | 'bad'> = [];
  while (Date.now() < deadline) {
    const r = await probeOnce(runner, cfg.health_check.url, expectStatus);
    if (r === 'infra') break;
    results.push(r === 'ok' ? 'ok' : 'bad');
    await sleep(intervalMs);
  }

  const badCount = results.filter((r) => r === 'bad').length;
  const total = results.length || 1;
  const availability = (total - badCount) / total;
  // Allow one transient blip (a single failed probe during the kill) but
  // require the service to be green by the end of the window.
  const endGreen = results.length > 0 && results[results.length - 1] === 'ok';
  const passed = badCount <= 1 && endGreen;

  return {
    validatorId,
    status: passed ? 'PASS' : 'FAIL',
    observed: {
      backend: runner.backend,
      action: cfg.action,
      probes: results.length,
      failed_probes: badCount,
      availability_during_chaos: Number(availability.toFixed(4)),
      recovered: endGreen,
      chaos_detail: chaos.detail,
    },
    expected: {
      max_failed_probes: 1,
      end_state: 'healthy',
      recovery_timeout_ms: recoveryMs,
    },
    durationMs: Date.now() - start,
    evidenceRef: passed
      ? undefined
      : `service ${endGreen ? 'recovered but had' : 'did not recover;'} ${badCount} failed probe(s) of ${results.length} during "${cfg.action}"`,
  };
}

async function probeOnce(
  runner: ShellRunner,
  url: string,
  expectStatus: number,
): Promise<'ok' | 'bad' | 'infra'> {
  const r = await runner.run({
    argv: [
      'curl',
      '-s',
      '-o',
      '/dev/null',
      '-w',
      '%{http_code}',
      '--max-time',
      '5',
      url,
    ],
    timeoutMs: 10_000,
  });
  if (r.infraError) return 'infra';
  return r.stdout.trim() === String(expectStatus) ? 'ok' : 'bad';
}

interface ChaosOutcome {
  applied: boolean;
  infra: boolean;
  detail: string;
}

async function applyChaos(
  runner: ShellRunner,
  action: ChaosProbeConfig['action'],
  selector: string,
): Promise<ChaosOutcome> {
  const run = async (
    argv: string[],
  ): Promise<{ ok: boolean; infra: boolean; out: string }> => {
    const r = await runner.run({ argv, timeoutMs: 120_000 });
    return {
      ok: r.exitCode === 0,
      infra: Boolean(r.infraError),
      out: (r.stdout + r.stderr).slice(0, 400),
    };
  };

  switch (action) {
    case 'kill_pod': {
      const r = await run([
        'sh',
        '-c',
        `kubectl delete pod ${selector ? '-l ' + shq(selector) : '--all'} --grace-period=0 --force`,
      ]);
      return { applied: r.ok, infra: r.infra, detail: r.out };
    }
    case 'delete_deployment_replica': {
      // scale down by one, then back up (forces a replica churn).
      const r = await run([
        'sh',
        '-c',
        `kubectl get deploy ${selector ? '-l ' + shq(selector) : ''} -o name | head -1 | xargs -I{} sh -c 'kubectl scale {} --replicas=$(( $(kubectl get {} -o jsonpath="{.spec.replicas}") - 1 )); sleep 5; kubectl rollout restart {}'`,
      ]);
      return { applied: r.ok, infra: r.infra, detail: r.out };
    }
    case 'drain_node':
    case 'cordon_node': {
      const cmd =
        action === 'drain_node'
          ? `kubectl drain ${selector || '$(kubectl get nodes -o name | head -1)'} --ignore-daemonsets --delete-emptydir-data --force --timeout=60s`
          : `kubectl cordon ${selector || '$(kubectl get nodes -o name | head -1)'}`;
      const r = await run(['sh', '-c', cmd]);
      return { applied: r.ok, infra: r.infra, detail: r.out };
    }
    case 'sever_dependency': {
      // apply a deny-all NetworkPolicy to the selected pods for the window.
      const np = `
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: chaos-sever-dependency
spec:
  podSelector: ${selector ? `{ matchLabels: { ${selector.replace('=', ': ')} } }` : '{}'}
  policyTypes: [Egress]
  egress: []
`.trim();
      const r = await run([
        'sh',
        '-c',
        `cat <<'EOF' | kubectl apply -f -\n${np}\nEOF`,
      ]);
      return { applied: r.ok, infra: r.infra, detail: r.out };
    }
    default:
      return {
        applied: false,
        infra: false,
        detail: `unknown action ${String(action)}`,
      };
  }
}

function shq(s: string): string {
  return `'${s.replace(/'/g, `'\\''`)}'`;
}
function sleep(ms: number): Promise<void> {
  return new Promise((r) => setTimeout(r, ms));
}
