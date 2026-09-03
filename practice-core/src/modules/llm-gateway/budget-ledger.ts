import { BudgetExhaustedError } from './types';

/**
 * Doc §7.6 Budgeting: "Per-attempt token budget, per-learner daily
 * budget, per-tenant monthly budget, global circuit breaker. On budget
 * exhaustion, degrade gracefully to authored static hints -- never fail
 * hard."
 *
 * In-memory rolling counters keyed by scope id + window. A real
 * deployment persists these (the `budget` table / a Redis counter) and
 * shares them across replicas; the check/charge interface is unchanged.
 * The global circuit breaker also trips on a burst of provider errors
 * (tripGlobal) so a provider meltdown degrades everyone to static hints
 * rather than each call timing out.
 */

export interface BudgetConfig {
  perAttemptUsd: number;
  perUserDailyUsd: number;
  perTenantMonthlyUsd: number;
  globalDailyUsd: number;
}

const DEFAULTS: BudgetConfig = {
  perAttemptUsd: 0.05,
  perUserDailyUsd: 0.5,
  perTenantMonthlyUsd: 50,
  globalDailyUsd: 500,
};

interface Counter {
  spentUsd: number;
  windowStart: number;
}

export class BudgetLedger {
  private readonly cfg: BudgetConfig;
  private readonly attempt = new Map<string, Counter>();
  private readonly user = new Map<string, Counter>();
  private readonly tenant = new Map<string, Counter>();
  private global: Counter = { spentUsd: 0, windowStart: Date.now() };

  private globalBreakerUntil = 0;
  private consecutiveProviderErrors = 0;

  constructor(cfg: Partial<BudgetConfig> = {}) {
    this.cfg = { ...DEFAULTS, ...cfg };
  }

  private roll(c: Counter | undefined, windowMs: number): Counter {
    const now = Date.now();
    if (!c || now - c.windowStart >= windowMs) {
      return { spentUsd: 0, windowStart: now };
    }
    return c;
  }

  /**
   * Throws BudgetExhaustedError if `estCostUsd` would breach any scope,
   * OR if the global circuit breaker is open. Callers catch this and
   * degrade to static hints.
   */
  checkOrThrow(
    estCostUsd: number,
    scope: { attemptId?: string; userId?: string; tenantId?: string },
  ): void {
    if (Date.now() < this.globalBreakerUntil) {
      throw new BudgetExhaustedError('global');
    }

    const DAY = 86_400_000;
    const MONTH = 30 * DAY;

    const g = this.roll(this.global, DAY);
    if (g.spentUsd + estCostUsd > this.cfg.globalDailyUsd) {
      throw new BudgetExhaustedError('global');
    }

    if (scope.attemptId) {
      const c = this.roll(this.attempt.get(scope.attemptId), DAY);
      if (c.spentUsd + estCostUsd > this.cfg.perAttemptUsd) {
        throw new BudgetExhaustedError('attempt');
      }
    }
    if (scope.userId) {
      const c = this.roll(this.user.get(scope.userId), DAY);
      if (c.spentUsd + estCostUsd > this.cfg.perUserDailyUsd) {
        throw new BudgetExhaustedError('user');
      }
    }
    if (scope.tenantId) {
      const c = this.roll(this.tenant.get(scope.tenantId), MONTH);
      if (c.spentUsd + estCostUsd > this.cfg.perTenantMonthlyUsd) {
        throw new BudgetExhaustedError('tenant');
      }
    }
  }

  /** Record actual spend after a successful call. */
  charge(
    costUsd: number,
    scope: { attemptId?: string; userId?: string; tenantId?: string },
  ): void {
    const DAY = 86_400_000;
    const MONTH = 30 * DAY;

    this.global = this.roll(this.global, DAY);
    this.global.spentUsd += costUsd;

    if (scope.attemptId) {
      const c = this.roll(this.attempt.get(scope.attemptId), DAY);
      c.spentUsd += costUsd;
      this.attempt.set(scope.attemptId, c);
    }
    if (scope.userId) {
      const c = this.roll(this.user.get(scope.userId), DAY);
      c.spentUsd += costUsd;
      this.user.set(scope.userId, c);
    }
    if (scope.tenantId) {
      const c = this.roll(this.tenant.get(scope.tenantId), MONTH);
      c.spentUsd += costUsd;
      this.tenant.set(scope.tenantId, c);
    }
    this.consecutiveProviderErrors = 0;
  }

  /** Provider error -- after N in a row, open the global breaker briefly. */
  noteProviderError(): void {
    this.consecutiveProviderErrors++;
    if (this.consecutiveProviderErrors >= 5) {
      this.globalBreakerUntil = Date.now() + 60_000; // 1 min cooldown
    }
  }

  spentOnAttempt(attemptId: string): number {
    return this.roll(this.attempt.get(attemptId), 86_400_000).spentUsd;
  }

  get breakerOpen(): boolean {
    return Date.now() < this.globalBreakerUntil;
  }
}
