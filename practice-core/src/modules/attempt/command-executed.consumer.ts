import { Inject, Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import type { Kysely } from 'kysely';
import { NatsSubscriberBase } from '../../common/nats-subscriber-base';
import { KYSELY } from '../../db/database.module';
import type { Database } from '../../db/schema';
import { appendTypedEvent } from '../event-store/attempt-event-type';
import { EventStoreRepository } from '../event-store/event-store.repository';
import type {
  ActivitySpec,
  ActivitySpecProcessSignals,
} from '../catalog/activity-spec';
import { ActivitySpecReader } from '../../common/activity-spec-reader';

interface CommandExecutedEnvelope {
  attempt_id: string;
  payload: {
    cmd: string;
    exit_code: number;
    duration_ms: number;
    cmd_hash: string;
    source: string;
    cwd?: string;
  };
}

/**
 * Doc §3.3/§4.2, PLAN.md Phase 2: "blast_radius forbidden commands are
 * detected in Dev A's Session Broker tap but scored by Dev B's scoring
 * engine -- same event-based handoff as Phase 1's COMMAND_EXECUTED, no
 * new mechanism needed." That handoff mechanism (a NATS consumer on
 * this side) didn't exist at all before this -- COMMAND_EXECUTED was
 * published by the orchestrator's telemetry tap
 * (orchestrator/internal/telemetry/nats_sink.go) but nothing ever
 * subscribed practice-core-side, so command text never reached
 * attempt_events or any scoring signal despite the whole pipeline up to
 * this point being built.
 *
 * Two responsibilities per event:
 *  1. Append the raw event to attempt_events (doc §4.2's own contract:
 *     attempt_events is the sink for every event, and this is the only
 *     writer for COMMAND_EXECUTED specifically -- it was simply never
 *     reaching a writer before).
 *  2. Doc §6.4: "scoring reads signals, never raw events" -- check the
 *     command against the activity's process_signals (blast_radius.
 *     forbidden, diagnostic_efficiency.good/bad_actions) and upsert
 *     running counts into attempt_signal, which is what the scoring
 *     engine actually reads at evaluate() time (fn.blast_radius.v1,
 *     fn.diagnostic_efficiency.v1).
 */
@Injectable()
export class CommandExecutedConsumer extends NatsSubscriberBase<CommandExecutedEnvelope> {
  protected readonly logger = new Logger(CommandExecutedConsumer.name);
  protected readonly subject = 'env.telemetry.COMMAND_EXECUTED';

  constructor(
    @Inject(KYSELY) private readonly db: Kysely<Database>,
    config: ConfigService,
    private readonly events: EventStoreRepository,
    private readonly specReader: ActivitySpecReader,
  ) {
    super(config);
  }

  protected isValidEnvelope(
    envelope: CommandExecutedEnvelope,
    raw: string,
  ): boolean {
    if (!envelope.attempt_id || !envelope.payload) {
      this.logger.warn(
        `COMMAND_EXECUTED message missing attempt_id/payload, dropping: ${raw}`,
      );
      return false;
    }
    return true;
  }

  protected async handleMessage(
    envelope: CommandExecutedEnvelope,
  ): Promise<void> {
    await this.handleCommand(envelope.attempt_id, envelope.payload);
  }

  private async handleCommand(
    attemptId: string,
    payload: CommandExecutedEnvelope['payload'],
  ): Promise<void> {
    await appendTypedEvent(this.events, {
      attemptId,
      actor: 'LEARNER',
      type: 'COMMAND_EXECUTED',
      payload,
    });

    const signals = await this.processSignalsFor(attemptId);
    if (!signals) return; // GUIDED_LAB attempts (no process_signals authored) have nothing to check

    const cmd = payload.cmd;

    if (
      signals.blast_radius?.forbidden?.some((forbidden) =>
        cmd.includes(forbidden),
      )
    ) {
      await this.incrementSignal(attemptId, 'blast_radius_violations');
    }

    const de = signals.diagnostic_efficiency;
    if (de?.good_actions?.some((good) => cmd.includes(good))) {
      await this.incrementSignal(attemptId, 'diagnostic_efficiency_good_count');
    }
    if (de?.bad_actions?.some((bad) => cmd.includes(bad))) {
      await this.incrementSignal(attemptId, 'diagnostic_efficiency_bad_count');
    }
  }

  private async processSignalsFor(
    attemptId: string,
  ): Promise<ActivitySpecProcessSignals | null> {
    const specRaw = await this.specReader.getActivitySpec(attemptId);
    if (!specRaw) return null;
    const spec = specRaw as Pick<ActivitySpec, 'process_signals'>;
    return spec.process_signals ?? null;
  }

  private async incrementSignal(
    attemptId: string,
    signalKey: string,
  ): Promise<void> {
    await this.db
      .insertInto('attempt.attempt_signal')
      .values({ attempt_id: attemptId, signal_key: signalKey, value_num: 1 })
      .onConflict((oc) =>
        oc.columns(['attempt_id', 'signal_key']).doUpdateSet((eb) => ({
          value_num: eb('attempt.attempt_signal.value_num', '+', 1),
          computed_at: new Date(),
        })),
      )
      .execute();
  }
}
