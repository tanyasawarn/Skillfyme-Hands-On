import { Module } from '@nestjs/common';
import { ConfigModule } from '@nestjs/config';
import { APP_GUARD } from '@nestjs/core';
import { ThrottlerGuard, ThrottlerModule } from '@nestjs/throttler';
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
import { MetricsModule } from './modules/metrics/metrics.module';

@Module({
  imports: [
    ConfigModule.forRoot({ isGlobal: true }),
    // Phase 3 standardization: previously nothing throttled repeated
    // calls to any endpoint, including /v1/auth/dev-login (@Public(),
    // no password, mints a real 12h-valid signed JWT on every call --
    // the single highest-value target to rate-limit in this app) and
    // hint-reveal (each reveal call has a real scoring-penalty side
    // effect, so unthrottled access is also a scoring-integrity concern,
    // not just a resource-abuse one). This default (100 req/60s per
    // client IP) applies globally via the APP_GUARD registration below;
    // AuthController.devLogin has a tighter override (see that file).
    ThrottlerModule.forRoot([{ ttl: 60_000, limit: 100 }]),
    DatabaseModule,
    AuthModule,
    MetricsModule,
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
    // Ordering matters: NestJS runs APP_GUARDs in registration order.
    // ThrottlerGuard runs FIRST, before AuthGuard -- rate limiting must
    // apply to unauthenticated/pre-auth traffic too (dev-login itself is
    // @Public(), so if AuthGuard ran first it would never even see this
    // request; the whole point is throttling reaches every request
    // regardless of auth outcome).
    { provide: APP_GUARD, useClass: ThrottlerGuard },
    { provide: APP_GUARD, useClass: AuthGuard },
    { provide: APP_GUARD, useClass: RolesGuard },
  ],
})
export class AppModule {}
