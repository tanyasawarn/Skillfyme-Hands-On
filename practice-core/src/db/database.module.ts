import { Global, Inject, Module, OnModuleDestroy } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { Kysely, PostgresDialect } from 'kysely';
import { Pool } from 'pg';
import type { Database } from './schema';

export const KYSELY = Symbol('KYSELY_DB');

@Global()
@Module({
  providers: [
    {
      provide: KYSELY,
      inject: [ConfigService],
      useFactory: (config: ConfigService) => {
        const pool = new Pool({
          connectionString: config.get<string>('DATABASE_URL'),
          max: 10,
        });
        return new Kysely<Database>({ dialect: new PostgresDialect({ pool }) });
      },
    },
  ],
  exports: [KYSELY],
})
export class DatabaseModule implements OnModuleDestroy {
  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  /**
   * Drain the pg pool on shutdown. Without this, a Nest app context that
   * built this module (integration tests via Test.createTestingModule,
   * one-off scripts) leaves an idle-timeout timer + open socket that
   * outlives the process's work — visible as jest's "did not exit"
   * warning. `Kysely.destroy()` calls `pool.end()` underneath.
   */
  async onModuleDestroy(): Promise<void> {
    await this.db.destroy();
  }
}
