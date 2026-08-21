import { Global, Module, OnModuleDestroy } from '@nestjs/common';
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
  constructor() {}

  async onModuleDestroy() {
    // Kysely/pg pool teardown is handled per-instance; explicit destroy hook
    // left here so services relying on graceful shutdown (doc's reaper-style
    // "assume the process can die mid-op" discipline) have a place to hook in
    // once this module owns more than a single pool.
  }
}
