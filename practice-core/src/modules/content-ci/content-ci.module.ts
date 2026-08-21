import { Module } from '@nestjs/common';
import { SpecLintService } from './spec-lint.service';

@Module({
  providers: [SpecLintService],
  exports: [SpecLintService],
})
export class ContentCiModule {}
