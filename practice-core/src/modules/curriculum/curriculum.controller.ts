import { Controller, Get, Param } from '@nestjs/common';
import { CurriculumRepository } from './curriculum.repository';
import { AuthUser } from '../auth/auth-user.decorator';
import type { AuthClaims } from '../auth/auth.types';
import { findOrThrow } from '../../common/find-or-throw';

/** Doc §1.2: catalog navigation reads the curriculum tree to build course/topic filters. */
@Controller('v1/practice/courses')
export class CurriculumController {
  constructor(private readonly curriculum: CurriculumRepository) {}

  @Get()
  async list(@AuthUser() auth: AuthClaims) {
    return this.curriculum.listCourses(auth.tenantId);
  }

  @Get(':slug')
  async getTree(@AuthUser() auth: AuthClaims, @Param('slug') slug: string) {
    return findOrThrow(
      await this.curriculum.getCourseTree(auth.tenantId, slug),
      `course ${slug} not found`,
    );
  }
}
