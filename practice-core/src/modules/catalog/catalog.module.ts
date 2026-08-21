import { Module } from '@nestjs/common';
import { CatalogRepository } from './catalog.repository';
import { CatalogController } from './catalog.controller';

@Module({
  providers: [CatalogRepository],
  controllers: [CatalogController],
  exports: [CatalogRepository],
})
export class CatalogModule {}
