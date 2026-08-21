import { Injectable, Logger } from '@nestjs/common';
import { randomUUID } from 'node:crypto';
import type {
  ConnectRequest,
  ConnectResult,
  DestroyRequest,
  ExecShellRequest,
  ExecShellResult,
  OrchestratorClient,
  ProvisionRequest,
  ProvisionResult,
} from './orchestrator-client.interface';

/**
 * PLAN.md Phase 0: "Dev B builds against a fake Orchestrator (returns
 * READY after a fixed delay) until Dev A's is live." This stands in for
 * contracts/orchestrator.proto's Provision/Destroy RPCs so AttemptService
 * and its tests don't block on Dev A's Go service existing.
 */
@Injectable()
export class FakeOrchestratorClient implements OrchestratorClient {
  private readonly logger = new Logger(FakeOrchestratorClient.name);
  private readonly fixedDelayMs: number;

  constructor(fixedDelayMs = 50) {
    this.fixedDelayMs = fixedDelayMs;
  }

  async provision(req: ProvisionRequest): Promise<ProvisionResult> {
    this.logger.debug(
      `[fake] provisioning ${req.tier} for attempt ${req.attemptId}`,
    );
    await sleep(this.fixedDelayMs);
    return { environmentId: `fake-env-${randomUUID()}`, status: 'READY' };
  }

  async destroy(req: DestroyRequest): Promise<{ alreadyDestroyed: boolean }> {
    this.logger.debug(`[fake] destroying ${req.environmentId} (${req.reason})`);
    await sleep(this.fixedDelayMs);
    return { alreadyDestroyed: false };
  }

  async connect(req: ConnectRequest): Promise<ConnectResult> {
    this.logger.debug(`[fake] connect ${req.environmentId}`);
    await sleep(this.fixedDelayMs);
    return {
      terminalWsUrl: `ws://fake-gateway.invalid/v1/env/${req.environmentId}/terminal?session=fake-${randomUUID()}`,
      editorUrl: '',
      sessionToken: `fake-${randomUUID()}`,
      expiresAt: new Date(Date.now() + 5 * 60_000).toISOString(),
    };
  }

  async execShell(req: ExecShellRequest): Promise<ExecShellResult> {
    this.logger.debug(
      `[fake] execShell on ${req.environmentId}: ${req.command.slice(0, 80)}`,
    );
    await sleep(this.fixedDelayMs);
    // No real pod backs a fake environment -- always report "no such
    // file" rather than fabricating file content, so the Monaco file
    // tree honestly renders empty/errored in local dev without a real
    // orchestrator, instead of silently showing fake data.
    return {
      exitCode: 1,
      stdout: '',
      stderr: 'fake orchestrator: no filesystem backing this environment',
    };
  }
}

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}
