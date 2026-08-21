import { Module } from '@nestjs/common';
import { CurriculumRepository } from './curriculum.repository';
import { CurriculumController } from './curriculum.controller';

@Module({
  providers: [CurriculumRepository],
  controllers: [CurriculumController],
  exports: [CurriculumRepository],
})
export class CurriculumModule {}
