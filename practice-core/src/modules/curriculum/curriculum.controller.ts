import { Controller, Get, NotFoundException, Param } from '@nestjs/common';
import { CurriculumRepository } from './curriculum.repository';
import { AuthUser } from '../auth/auth-user.decorator';
import type { AuthClaims } from '../auth/auth.types';

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
    const tree = await this.curriculum.getCourseTree(auth.tenantId, slug);
    if (!tree) throw new NotFoundException(`course ${slug} not found`);
    return tree;
  }
}
