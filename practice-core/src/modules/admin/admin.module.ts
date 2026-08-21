import { Module } from '@nestjs/common';
import { AnalyticsService } from './analytics.service';
import { AdminController } from './admin.controller';
import { CatalogModule } from '../catalog/catalog.module';
import { ContentCiModule } from '../content-ci/content-ci.module';

@Module({
  imports: [CatalogModule, ContentCiModule],
  providers: [AnalyticsService],
  controllers: [AdminController],
})
export class AdminModule {}
