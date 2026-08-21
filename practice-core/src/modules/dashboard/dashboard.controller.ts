import { Controller, Get, Query } from '@nestjs/common';
import { RecommendationService } from '../recommendation/recommendation.service';
import { MasteryService } from '../skill/mastery.service';
import { AttemptRepository } from '../attempt/attempt.repository';
import { AuthUser } from '../auth/auth-user.decorator';
import type { AuthClaims } from '../auth/auth.types';

/**
 * Doc §1.2: "Home is a decision surface, not a dashboard. Its job is to
 * make the next click obvious. Maximum three primary CTAs: Continue
 * attempt, Recommended next, Fix a weak skill." This endpoint composes
 * the three data sources the doc lists (Continue / Recommended / Mastery
 * snapshot / Recent) into one call so the frontend's Home page isn't
 * firing four separate requests -- it doesn't own any data itself, only
 * reads from the services that do (Recommendation, Mastery, Attempt).
 */
@Controller('v1/practice/dashboard')
export class DashboardController {
  constructor(
    private readonly recommendations: RecommendationService,
    private readonly mastery: MasteryService,
    private readonly attempts: AttemptRepository,
  ) {}

  @Get()
  async get(
    @AuthUser() auth: AuthClaims,
    @Query('course') courseSlug: string = 'devops-with-ai',
  ) {
    const [recommended, masterySnapshot, recentAttempts, inProgress] =
      await Promise.all([
        this.recommendations.recommend(auth.userId, auth.tenantId, 3),
        this.mastery.listMasteryForUser(auth.userId, courseSlug),
        this.attempts.listForUser(auth.userId),
        this.attempts.listForUser(auth.userId, [
          'CREATED',
          'PROVISIONING',
          'READY',
          'IN_PROGRESS',
          'SUSPENDED',
        ]),
      ]);

    return {
      continueAttempt: inProgress[0] ?? null,
      recommended,
      masterySnapshot: masterySnapshot.slice(0, 5),
      recent: recentAttempts.slice(0, 5),
    };
  }
}
