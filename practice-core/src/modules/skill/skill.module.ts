import { Module } from '@nestjs/common';
import { SkillRepository } from './skill.repository';
import { BktService } from './bkt.service';
import { MasteryService } from './mastery.service';
import { SkillController } from './skill.controller';

@Module({
  controllers: [SkillController],
  providers: [SkillRepository, BktService, MasteryService],
  exports: [SkillRepository, BktService, MasteryService],
})
export class SkillModule {}
