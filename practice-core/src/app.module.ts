import { Module } from '@nestjs/common';
import { ConfigModule } from '@nestjs/config';
import { APP_GUARD } from '@nestjs/core';
import { AppController } from './app.controller';
import { AppService } from './app.service';
import { DatabaseModule } from './db/database.module';
import { SkillModule } from './modules/skill/skill.module';
import { CatalogModule } from './modules/catalog/catalog.module';
import { EventStoreModule } from './modules/event-store/event-store.module';
import { AttemptModule } from './modules/attempt/attempt.module';
import { EvaluationModule } from './modules/evaluation/evaluation.module';
import { CurriculumModule } from './modules/curriculum/curriculum.module';
import { RecommendationModule } from './modules/recommendation/recommendation.module';
import { DashboardModule } from './modules/dashboard/dashboard.module';
import { AdminModule } from './modules/admin/admin.module';
import { AuthModule } from './modules/auth/auth.module';
import { AuthGuard } from './modules/auth/auth.guard';
import { RolesGuard } from './modules/auth/roles.guard';

@Module({
  imports: [
    ConfigModule.forRoot({ isGlobal: true }),
    DatabaseModule,
    AuthModule,
    SkillModule,
    CatalogModule,
    EventStoreModule,
    EvaluationModule,
    AttemptModule,
    CurriculumModule,
    RecommendationModule,
    DashboardModule,
    AdminModule,
  ],
  controllers: [AppController],
  providers: [
    AppService,
    { provide: APP_GUARD, useClass: AuthGuard },
    { provide: APP_GUARD, useClass: RolesGuard },
  ],
})
export class AppModule {}
