import { Injectable, Logger } from '@nestjs/common';
import { randomUUID } from 'node:crypto';
import type {
  ProjectEnvHandle,
  ProjectOrchestratorPort,
  ProjectSnapshotHandle,
} from './project-orchestrator.port';

/**
 * Phase 3 (1.6 / B2). Stand-in for the real T3 driver until Stage 3.2/3.4.
 * Always succeeds, returns plausible handles, keeps a little in-memory
 * state so a suspend→restore round-trip returns a consistent id. The
 * milestone state-machine tests drive this; the state machine itself is
 * written to not care which implementation it gets.
 */
@Injectable()
export class FakeProjectOrchestrator implements ProjectOrchestratorPort {
  private readonly logger = new Logger(FakeProjectOrchestrator.name);
  private readonly envs = new Map<string, ProjectEnvHandle>();
  private readonly snapshots = new Map<string, { environmentId: string }>();

  provisionForMilestone(input: {
    attemptId: string;
    milestoneKey: string;
  }): Promise<ProjectEnvHandle> {
    const environmentId = `fake-t3-${randomUUID()}`;
    const handle: ProjectEnvHandle = {
      environmentId,
      status: 'READY',
      editorUrl: `https://fake-openvscode/${environmentId}`,
      terminalWsUrl: `wss://fake-openvscode/${environmentId}/terminal`,
    };
    this.envs.set(environmentId, handle);
    this.logger.debug(
      `[fake] provisioned ${environmentId} for ${input.attemptId}/${input.milestoneKey}`,
    );
    return Promise.resolve(handle);
  }

  snapshotAndSuspend(input: {
    attemptId: string;
    environmentId: string;
  }): Promise<ProjectSnapshotHandle> {
    const snapshotId = `fake-snap-${randomUUID()}`;
    this.snapshots.set(snapshotId, { environmentId: input.environmentId });
    this.envs.delete(input.environmentId);
    this.logger.debug(
      `[fake] snapshot ${snapshotId} + suspend ${input.environmentId}`,
    );
    return Promise.resolve({
      snapshotId,
      capturedAt: new Date().toISOString(),
    });
  }

  restore(input: {
    attemptId: string;
    snapshotId: string;
  }): Promise<ProjectEnvHandle> {
    const snap = this.snapshots.get(input.snapshotId);
    const environmentId = snap?.environmentId ?? `fake-t3-${randomUUID()}`;
    const handle: ProjectEnvHandle = {
      environmentId,
      status: 'READY',
      editorUrl: `https://fake-openvscode/${environmentId}`,
      terminalWsUrl: `wss://fake-openvscode/${environmentId}/terminal`,
    };
    this.envs.set(environmentId, handle);
    this.logger.debug(
      `[fake] restored ${environmentId} from ${input.snapshotId}`,
    );
    return Promise.resolve(handle);
  }

  destroy(input: {
    attemptId: string;
    environmentId: string;
  }): Promise<{ alreadyDestroyed: boolean }> {
    const existed = this.envs.delete(input.environmentId);
    this.logger.debug(`[fake] destroyed ${input.environmentId}`);
    return Promise.resolve({ alreadyDestroyed: !existed });
  }
}
