import { Global, Module } from '@nestjs/common';
import { MetricsController } from './metrics.controller';
import { MetricsService } from './metrics.service';

/**
 * @Global so any service (attempt, evaluation, recommendation, skill)
 * can inject MetricsService without re-importing this module -- the same
 * pattern AuthModule uses. Exposes GET /metrics for Prometheus.
 */
@Global()
@Module({
  controllers: [MetricsController],
  providers: [MetricsService],
  exports: [MetricsService],
})
export class MetricsModule {}
