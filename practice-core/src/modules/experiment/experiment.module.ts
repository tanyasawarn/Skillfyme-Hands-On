import { Module } from '@nestjs/common';
import { ExperimentService } from './experiment.service';
import { NorthStarService } from './north-star.service';

/**
 * PLAN.md G11 / doc §11.4 -- A/B framework + north-star instrumentation.
 * DatabaseModule is @Global so KYSELY is available. Exported so the
 * recommendation / mentor / hint paths can call assign(), and admin
 * analytics can read metrics().
 */
@Module({
  providers: [ExperimentService, NorthStarService],
  exports: [ExperimentService, NorthStarService],
})
export class ExperimentModule {}
