import { Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { NatsSubscriberBase } from '../../common/nats-subscriber-base';
import { AttemptService } from './attempt.service';

interface EnvDestroyedEnvelope {
  attempt_id: string;
  payload: {
    environment_id: string;
    reason: 'submit' | 'idle' | 'ttl' | 'budget' | 'reaper' | 'admin';
    snapshot_id?: string;
  };
}

/**
 * Doc §4.2 / contracts/events.md rule #3: "ENV_DESTROYED is the only way
 * an attempt learns its environment is gone, whether that's a clean
 * submit teardown, idle/TTL/budget teardown, or the reaper
 * force-destroying past a deadline." PLAN.md integration point #4.
 *
 * Before this existed, only the submit path (AttemptService.submit()
 * itself calling orchestrator.destroy()) ever reached
 * AttemptService.handleEnvironmentDestroyed() -- an idle-timeout or
 * TTL-expired environment left the attempt stuck IN_PROGRESS forever
 * server-side (the orchestrator tore the real infrastructure down, but
 * nothing told Practice Core), which meant EligibilityService's
 * concurrent-environment quota kept counting a phantom active attempt
 * and permanently blocked the learner from starting their next lab.
 * This consumer is the missing half: it subscribes to the Orchestrator's
 * NATS publish (internal/orchestrator/destroyer.go) and routes every
 * reason -- not just submit -- through the same idempotent handler.
 */
@Injectable()
export class EnvDestroyedConsumer extends NatsSubscriberBase<EnvDestroyedEnvelope> {
  protected readonly logger = new Logger(EnvDestroyedConsumer.name);
  protected readonly subject = 'env.telemetry.ENV_DESTROYED';

  constructor(
    config: ConfigService,
    private readonly attempts: AttemptService,
  ) {
    super(config);
  }

  protected isValidEnvelope(
    envelope: EnvDestroyedEnvelope,
    raw: string,
  ): boolean {
    if (!envelope.attempt_id) {
      this.logger.warn(
        `ENV_DESTROYED message missing attempt_id, dropping: ${raw}`,
      );
      return false;
    }
    return true;
  }

  // A malformed message must not crash the consumer loop (see
  // NatsSubscriberBase's own doc comment) -- every subsequent
  // ENV_DESTROYED would then also go unhandled, which is exactly the
  // "attempt stuck forever" failure mode this consumer exists to fix.
  protected async handleMessage(envelope: EnvDestroyedEnvelope): Promise<void> {
    await this.attempts.handleEnvironmentDestroyed(
      envelope.attempt_id,
      envelope.payload.reason,
    );
  }
}
