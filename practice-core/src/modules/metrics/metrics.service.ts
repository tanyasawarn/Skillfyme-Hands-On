import { Injectable } from '@nestjs/common';
import {
  Registry,
  Counter,
  Histogram,
  collectDefaultMetrics,
} from '@prometheus-io/client';

/**
 * Practice Core's Prometheus instrumentation surface (doc §11
 * "Observability and admin analytics", PLAN.md Phase 1 exit-criteria
 * measurement). The orchestrator owns provision/cost/reaper metrics
 * (orchestrator/internal/metrics); this owns the product-side ones the
 * exit criteria also need:
 *
 *   - validator ERROR rate < 0.5%  -> validatorResultTotal{status="ERROR"}
 *   - attempt throughput / funnel  -> attemptTransitionTotal{to=}
 *   - scoring + recommendation latency (doc §11 admin analytics)
 *
 * One Registry owned here (not the global default) so tests don't leak
 * counters between suites and the endpoint renders exactly this app's
 * metrics. `attempt_id` is deliberately NOT a label anywhere -- it's
 * unbounded cardinality; correlation to a specific attempt is via logs
 * (doc §13.5 #1), not metric labels.
 */
@Injectable()
export class MetricsService {
  readonly registry = new Registry();

  readonly attemptTransitionTotal = new Counter({
    name: 'practice_core_attempt_transition_total',
    help: 'Attempt state-machine transitions, by destination status.',
    labelNames: ['to'] as const,
    registers: [this.registry],
  });

  readonly validatorResultTotal = new Counter({
    name: 'practice_core_validator_result_total',
    help: 'Validator Runner outcomes by type and status (PASS/FAIL/ERROR/SKIP). ERROR rate = ERROR / total, exit criterion < 0.5%.',
    labelNames: ['validator_type', 'status'] as const,
    registers: [this.registry],
  });

  readonly scoringDurationSeconds = new Histogram({
    name: 'practice_core_scoring_duration_seconds',
    help: 'Time to run the signals->criteria->profile scoring pipeline for one attempt.',
    buckets: [0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5],
    registers: [this.registry],
  });

  readonly recommendationDurationSeconds = new Histogram({
    name: 'practice_core_recommendation_duration_seconds',
    help: 'Time to produce a recommendation set (candidate gen -> eligibility -> scoring).',
    buckets: [0.01, 0.05, 0.1, 0.25, 0.5, 1, 2, 5],
    registers: [this.registry],
  });

  constructor() {
    // Node process metrics (event-loop lag, GC, heap, fd count) prefixed
    // so they don't collide with anything the orchestrator exposes.
    collectDefaultMetrics({
      register: this.registry,
      prefix: 'practice_core_',
    });
  }

  recordAttemptTransition(toStatus: string): void {
    this.attemptTransitionTotal.inc({ to: toStatus });
  }

  recordValidatorResult(validatorType: string, status: string): void {
    this.validatorResultTotal.inc({
      validator_type: validatorType,
      status,
    });
  }

  /** Returns the text-exposition-format metrics body for the /metrics route. */
  async render(): Promise<{ contentType: string; body: string }> {
    return {
      contentType: this.registry.contentType,
      body: await this.registry.metrics(),
    };
  }
}
