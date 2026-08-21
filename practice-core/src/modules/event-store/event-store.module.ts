import { Module } from '@nestjs/common';
import { EventStoreRepository } from './event-store.repository';
import { ReplayService } from './replay.service';

@Module({
  providers: [EventStoreRepository, ReplayService],
  exports: [EventStoreRepository, ReplayService],
})
export class EventStoreModule {}
