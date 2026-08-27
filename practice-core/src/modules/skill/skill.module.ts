import { Module } from '@nestjs/common';
import { SkillRepository } from './skill.repository';
import { BktService } from './bkt.service';
import { EloService } from './elo.service';
import { MasteryService } from './mastery.service';
import { SkillController } from './skill.controller';

@Module({
  controllers: [SkillController],
  providers: [SkillRepository, BktService, EloService, MasteryService],
  exports: [SkillRepository, BktService, EloService, MasteryService],
})
export class SkillModule {}
