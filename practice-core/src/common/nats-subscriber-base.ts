import { Logger, OnModuleDestroy, OnModuleInit } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import {
  connect,
  type NatsConnection,
  type Subscription,
} from '@nats-io/transport-node';

/**
 * PLAN.md Phase 3's S4: connect/subscribe/drain/close lifecycle was
 * duplicated wholesale between CommandExecutedConsumer and
 * EnvDestroyedConsumer -- both real NATS consumers subscribing to a
 * single subject, decoding+JSON-parsing each message, and routing to a
 * per-consumer handler, with identical malformed-message-must-not-crash-
 * the-loop error handling.
 *
 * Template-method shape: this base owns connect/subscribe/loop/drain/
 * close and the decode+parse+dispatch+catch wrapper; each subclass
 * supplies its subject, its envelope type (via the generic), and its
 * handleMessage()/isValidEnvelope() logic. The one real behavioral
 * difference between the two original consumers -- what counts as a
 * "malformed, drop this message" envelope (EnvDestroyedConsumer checks
 * only `!attempt_id`; CommandExecutedConsumer also requires `payload` to
 * be present) -- is exactly why isValidEnvelope stays an abstract
 * per-subclass hook rather than a fixed check in the base: unifying it
 * to the stricter check would silently start dropping valid
 * ENV_DESTROYED messages that legitimately have no extra payload
 * fields; unifying to the looser check would let CommandExecutedConsumer
 * crash on a payload-less message it previously caught safely.
 */
export abstract class NatsSubscriberBase<TEnvelope>
  implements OnModuleInit, OnModuleDestroy
{
  protected abstract readonly logger: Logger;
  protected abstract readonly subject: string;

  private nc: NatsConnection | null = null;
  private sub: Subscription | null = null;

  constructor(protected readonly config: ConfigService) {}

  async onModuleInit(): Promise<void> {
    const servers =
      this.config.get<string>('NATS_URL') ?? 'nats://localhost:4222';
    this.nc = await connect({ servers });
    this.sub = this.nc.subscribe(this.subject);
    this.logger.log(`Subscribed to ${this.subject} at ${servers}`);
    this.consume(this.sub);
  }

  async onModuleDestroy(): Promise<void> {
    await this.sub?.drain();
    await this.nc?.close();
  }

  /** True if envelope has everything handleMessage() needs -- an invalid envelope is logged and dropped, never passed to handleMessage(). */
  protected abstract isValidEnvelope(envelope: TEnvelope, raw: string): boolean;

  /** The per-consumer business logic for one valid, parsed message. */
  protected abstract handleMessage(envelope: TEnvelope): Promise<void>;

  private async consume(sub: Subscription): Promise<void> {
    const decoder = new TextDecoder();
    for await (const msg of sub) {
      const raw = decoder.decode(msg.data);
      try {
        const envelope = JSON.parse(raw) as TEnvelope;
        if (!this.isValidEnvelope(envelope, raw)) {
          continue;
        }
        await this.handleMessage(envelope);
      } catch (err) {
        // A malformed message must not crash the consumer loop -- every
        // subsequent message on this subject would then also go
        // unhandled, silently breaking whatever downstream mechanism
        // depends on this subscription staying alive.
        this.logger.error(
          `Failed to process ${this.subject} message: ${err instanceof Error ? err.message : err}`,
        );
      }
    }
  }
}
