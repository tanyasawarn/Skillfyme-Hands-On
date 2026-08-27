import { Inject, Injectable } from '@nestjs/common';
import type { Kysely } from 'kysely';
import { KYSELY } from '../db/database.module';
import type { Database } from '../db/schema';

/**
 * PLAN.md U6: every repository re-implemented the identical
 * `@Inject(KYSELY) private readonly db: Kysely<Database>` constructor.
 * Confirmed via grep across `src/modules/`: 5 real repository classes
 * (`SkillRepository`, `CurriculumRepository`, `CatalogRepository`,
 * `EventStoreRepository`, `AttemptRepository`) share this exact line
 * verbatim -- services with other dependencies alongside `KYSELY`
 * (`EligibilityService`, `EvaluationService`, etc.) are NOT repositories
 * in this sense and don't extend this.
 */
@Injectable()
export abstract class BaseRepository {
  constructor(@Inject(KYSELY) protected readonly db: Kysely<Database>) {}
}
