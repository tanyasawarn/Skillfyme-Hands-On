import { Controller, Get, Header, Res } from '@nestjs/common';
import type { Response } from 'express';
import { SkipThrottle } from '@nestjs/throttler';
import { Public } from '../auth/public.decorator';
import { MetricsService } from './metrics.service';

/**
 * GET /metrics -- Prometheus scrape endpoint (doc §11). @Public() because
 * a scraper carries no bearer JWT; @Throttle disabled because Prometheus
 * scrapes on a fixed short interval and must never be rate-limited into
 * gaps in the time series. In a real deployment this port/path is
 * reachable only from the monitoring network, not the public ingress --
 * same posture as the orchestrator's separate metrics port.
 */
@Controller()
export class MetricsController {
  constructor(private readonly metrics: MetricsService) {}

  @Public()
  @SkipThrottle()
  @Get('metrics')
  @Header('Cache-Control', 'no-store')
  async scrape(@Res() res: Response): Promise<void> {
    const { contentType, body } = await this.metrics.render();
    res.setHeader('Content-Type', contentType);
    res.send(body);
  }
}
