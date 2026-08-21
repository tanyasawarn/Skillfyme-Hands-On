import { Controller, Get, Query } from '@nestjs/common';
import { MasteryService } from './mastery.service';
import { AuthUser } from '../auth/auth-user.decorator';
import type { AuthClaims } from '../auth/auth.types';

/** Doc §8.3: GET /v1/practice/skills -- mastery per skill + band + evidence. */
@Controller('v1/practice/skills')
export class SkillController {
  constructor(private readonly mastery: MasteryService) {}

  @Get()
  async list(
    @AuthUser() auth: AuthClaims,
    @Query('course') courseSlug: string = 'devops-with-ai',
  ) {
    return this.mastery.listMasteryForUser(auth.userId, courseSlug);
  }
}
