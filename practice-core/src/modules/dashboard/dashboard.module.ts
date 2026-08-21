import { Module } from '@nestjs/common';
import { DashboardController } from './dashboard.controller';
import { RecommendationModule } from '../recommendation/recommendation.module';
import { SkillModule } from '../skill/skill.module';
import { AttemptModule } from '../attempt/attempt.module';

@Module({
  imports: [RecommendationModule, SkillModule, AttemptModule],
  controllers: [DashboardController],
})
export class DashboardModule {}
