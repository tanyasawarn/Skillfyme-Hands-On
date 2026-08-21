import { Controller, Get, Query } from '@nestjs/common';
import { RecommendationService } from './recommendation.service';
import { AuthUser } from '../auth/auth-user.decorator';
import type { AuthClaims } from '../auth/auth.types';

/** Doc §8.3: GET /v1/practice/recommendations?limit&context=home|skill|course. */
@Controller('v1/practice/recommendations')
export class RecommendationController {
  constructor(private readonly recommendations: RecommendationService) {}

  @Get()
  async list(@AuthUser() auth: AuthClaims, @Query('limit') limit?: string) {
    return this.recommendations.recommend(
      auth.userId,
      auth.tenantId,
      limit ? Number(limit) : undefined,
    );
  }
}
