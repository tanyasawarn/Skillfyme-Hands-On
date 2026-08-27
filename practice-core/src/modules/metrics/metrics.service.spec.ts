import { MetricsService } from './metrics.service';

describe('MetricsService', () => {
  let svc: MetricsService;

  beforeEach(() => {
    svc = new MetricsService();
  });

  it('renders text-exposition format with the custom metric names the alert rules query', async () => {
    svc.recordAttemptTransition('IN_PROGRESS');
    svc.recordValidatorResult('SHELL_ASSERT', 'PASS');
    svc.recordValidatorResult('K8S_ASSERT', 'ERROR');

    const { contentType, body } = await svc.render();

    expect(contentType).toContain('text/plain');
    expect(body).toContain('practice_core_attempt_transition_total');
    expect(body).toContain('practice_core_validator_result_total');
    expect(body).toContain('practice_core_scoring_duration_seconds');
    expect(body).toContain('practice_core_recommendation_duration_seconds');
    // default Node metrics come through with the configured prefix
    expect(body).toContain('practice_core_process_cpu_seconds_total');
  });

  it('labels attempt transitions by destination status', async () => {
    svc.recordAttemptTransition('PROVISIONING');
    svc.recordAttemptTransition('PROVISIONING');
    svc.recordAttemptTransition('SUBMITTED');

    const body = await svc.registry.metrics();
    expect(body).toMatch(
      /practice_core_attempt_transition_total\{to="PROVISIONING"\} 2/,
    );
    expect(body).toMatch(
      /practice_core_attempt_transition_total\{to="SUBMITTED"\} 1/,
    );
  });

  it('separates validator results by type and status so ERROR rate is computable per type', async () => {
    svc.recordValidatorResult('SHELL_ASSERT', 'PASS');
    svc.recordValidatorResult('SHELL_ASSERT', 'PASS');
    svc.recordValidatorResult('SHELL_ASSERT', 'ERROR');

    const body = await svc.registry.metrics();
    expect(body).toMatch(
      /practice_core_validator_result_total\{validator_type="SHELL_ASSERT",status="PASS"\} 2/,
    );
    expect(body).toMatch(
      /practice_core_validator_result_total\{validator_type="SHELL_ASSERT",status="ERROR"\} 1/,
    );
  });

  it('does not use attempt_id as a label anywhere (unbounded cardinality guard)', async () => {
    svc.recordAttemptTransition('IN_PROGRESS');
    svc.recordValidatorResult('FILE_EXISTS', 'PASS');
    const body = await svc.registry.metrics();
    expect(body).not.toContain('attempt_id=');
  });
});
