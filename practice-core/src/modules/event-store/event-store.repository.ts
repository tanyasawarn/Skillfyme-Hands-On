import { Inject, Injectable } from '@nestjs/common';
import { sql, type Kysely } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database, EventActor } from '../../db/schema';

export interface AppendEventInput {
  attemptId: string;
  actor: EventActor;
  type: string;
  payload: unknown;
  occurredAt?: Date;
}

export interface AttemptEventRow {
  attempt_id: string;
  seq: string;
  occurred_at: Date;
  actor: EventActor;
  type: string;
  payload: unknown;
}

/**
 * Doc §4.2: attempt_events is append-only, partitioned, with seq monotonic
 * per attempt. "seq is assigned by the writer (this service)" -- see
 * contracts/events.md. §13.5 #1: "The single most valuable observability
 * decision: attempt_id as a universal correlation key... build this on
 * day one." This repository is the one and only writer of attempt_events;
 * every other module that needs to record something appends through here
 * rather than writing to the table directly, so seq assignment is never
 * duplicated across call sites.
 */
@Injectable()
export class EventStoreRepository {
  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  /**
   * Appends one event, assigning seq = max(seq)+1 for the attempt.
   * Concurrent appends to the *same* attempt_id (e.g. Session Broker and
   * Practice Core both producing events for one attempt) are serialised
   * with a Postgres advisory lock keyed on attempt_id, held for the
   * transaction's duration, so seq assignment and insert are atomic
   * against any other writer targeting the same attempt. hashtext()
   * collapses the uuid to a 32-bit lock key; a collision between two
   * different attempt_ids only costs those two unrelated writers a brief
   * serialisation against each other, never a correctness issue.
   */
  async append(input: AppendEventInput): Promise<{ seq: string }> {
    return this.db.transaction().execute(async (trx) => {
      await sql`SELECT pg_advisory_xact_lock(hashtext(${input.attemptId}))`.execute(
        trx,
      );

      const row = await sql<{ seq: string }>`
        INSERT INTO attempt.attempt_events (attempt_id, seq, occurred_at, actor, type, payload)
        VALUES (
          ${input.attemptId},
          COALESCE(
            (SELECT max(seq) FROM attempt.attempt_events WHERE attempt_id = ${input.attemptId}),
            0
          ) + 1,
          ${input.occurredAt ?? new Date()},
          ${input.actor},
          ${input.type},
          ${JSON.stringify(input.payload)}::jsonb
        )
        RETURNING seq
      `.execute(trx);

      return { seq: row.rows[0].seq };
    });
  }

  /** Ordered replay for one attempt -- doc §4.4: "write a replay tool in week one." */
  async replay(attemptId: string): Promise<AttemptEventRow[]> {
    return this.db
      .selectFrom('attempt.attempt_events')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .orderBy('seq', 'asc')
      .execute();
  }

  async listSince(
    attemptId: string,
    afterSeq: string,
  ): Promise<AttemptEventRow[]> {
    return this.db
      .selectFrom('attempt.attempt_events')
      .selectAll()
      .where('attempt_id', '=', attemptId)
      .where('seq', '>', afterSeq)
      .orderBy('seq', 'asc')
      .execute();
  }
}
