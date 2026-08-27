import {
  Injectable,
  Logger,
  OnModuleDestroy,
  OnModuleInit,
} from '@nestjs/common';
import { AttemptRepository } from './attempt.repository';
import { AttemptService } from './attempt.service';
import { CacheSweepConstants } from '../../common/constants';

/**
 * Revised lifecycle requirement §3/§9's second stage: SUSPENDED -> CACHED
 * after a longer period of staying suspended. The first stage
 * (active -> suspended at 15 minutes) is NOT this sweep -- that already
 * runs end-to-end via the Go orchestrator's idle detector
 * (orchestrator/internal/idledetect/detector.go) publishing
 * ENV_DESTROYED(reason="idle"), consumed by
 * AttemptService.handleEnvironmentDestroyed. This sweep only ever reads
 * SUSPENDED attempts (AttemptRepository.findStaleSuspendedAttempts) --
 * by the time a row reaches here its environment is already gone, so
 * this stage is about history/cleanup, not cost control (SUSPENDED
 * already has zero backend cost).
 *
 * Mirrors the Go orchestrator's reaper (orchestrator/internal/reaper/
 * reaper.go): a plain setInterval sweep started in onModuleInit, not a
 * @nestjs/schedule cron job -- that package isn't a dependency here, and
 * a single fixed-interval loop is all this needs (no cron-expression
 * scheduling requirement).
 *
 * Soft-state only: cache() moves status to CACHED (no further
 * orchestrator.destroy() call needed -- SUSPENDED already tore the
 * environment down), but never deletes the attempt row or its event
 * history -- reactivate() (AttemptService) is the only way out of CACHED,
 * and it's idempotent by construction (attempt.service.ts's own comment).
 */
@Injectable()
export class CacheSweepService implements OnModuleInit, OnModuleDestroy {
  private readonly logger = new Logger(CacheSweepService.name);
  private timer?: ReturnType<typeof setInterval>;

  constructor(
    private readonly attempts: AttemptRepository,
    private readonly attemptService: AttemptService,
  ) {}

  onModuleInit() {
    this.timer = setInterval(
      () => void this.sweep(),
      CacheSweepConstants.SWEEP_INTERVAL_MS,
    );
    // unref so this timer alone doesn't keep the process alive (matches
    // how the rest of this service treats background loops as
    // best-effort, not load-bearing for shutdown).
    this.timer.unref?.();
  }

  onModuleDestroy() {
    if (this.timer) clearInterval(this.timer);
  }

  async sweep(): Promise<void> {
    const cutoff = new Date(
      Date.now() - CacheSweepConstants.INACTIVITY_THRESHOLD_MS,
    );
    const stale = await this.attempts.findStaleSuspendedAttempts(cutoff);
    for (const attempt of stale) {
      try {
        await this.attemptService.cache(attempt.id);
      } catch (err) {
        this.logger.error(
          `cache sweep failed for attempt ${attempt.id}: ${err}`,
        );
      }
    }
  }
}
