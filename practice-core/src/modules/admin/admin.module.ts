import { Module } from '@nestjs/common';
import { AnalyticsService } from './analytics.service';
import { CostDashboardService } from './cost-dashboard.service';
import { AdminController } from './admin.controller';
import { CatalogModule } from '../catalog/catalog.module';
import { ContentCiModule } from '../content-ci/content-ci.module';
import { AnalyticsModule } from '../analytics/analytics.module';

@Module({
  imports: [CatalogModule, ContentCiModule, AnalyticsModule],
  providers: [AnalyticsService, CostDashboardService],
  controllers: [AdminController],
})
export class AdminModule {}
