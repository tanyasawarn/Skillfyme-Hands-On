import { Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { BaseGrpcClient } from '../../common/base-grpc-client';
import type {
  ProjectEnvHandle,
  ProjectOrchestratorPort,
  ProjectSnapshotHandle,
} from './project-orchestrator.port';

/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 3.4 / IP-B2). The real
 * ProjectOrchestratorPort: translates the milestone state machine's
 * provision / snapshot-suspend / restore / destroy calls into
 * contracts/orchestrator.proto RPCs with tier=TIER_T3_CLOUD_ACCOUNT.
 *
 * Selected by PROJECT_ORCHESTRATOR_GRPC=on (project.module). Until the
 * orchestrator's T3 driver (Stage 3.2) and Snapshot/Restore impl
 * (Stage 3.3) land, these RPCs return UNIMPLEMENTED — which this adapter
 * surfaces as a thrown error, so the milestone state machine's own
 * try/catch logs and keeps the attempt moving (design milestone needs
 * no env; infra+ show the failure).
 *
 * A 2nd BaseGrpcClient subclass (like GrpcValidatorExecutor / the T3
 * shell runner) — evaluation/attempt/project modules each keep their own
 * client instance; the connection plumbing is shared.
 */
@Injectable()
export class GrpcProjectOrchestrator
  extends BaseGrpcClient
  implements ProjectOrchestratorPort
{
  protected readonly logger = new Logger(GrpcProjectOrchestrator.name);
  protected readonly protoFile = 'orchestrator.proto';
  protected readonly protoServicePath =
    'practiceengine.orchestrator.v1.EnvironmentOrchestrator';

  constructor(config: ConfigService) {
    super(config);
  }

  protected connectionLogMessage(address: string): string {
    return `project orchestrator adapter connecting to Environment Orchestrator at ${address}`;
  }

  async provisionForMilestone(input: {
    attemptId: string;
    milestoneKey: string;
  }): Promise<ProjectEnvHandle> {
    const res = await this.call<
      {
        attemptId: string;
        tier: string;
        blueprintId: string;
        blueprintVersion: string;
      },
      { environmentId?: string; status?: string; endpoints?: unknown }
    >(
      'Provision',
      {
        attemptId: input.attemptId,
        tier: 'TIER_T3_CLOUD_ACCOUNT',
        blueprintId: 'bp.project.default',
        blueprintVersion: 'v1',
      },
      120_000,
    );
    const endpoints = (res.endpoints ?? {}) as {
      editorUrl?: string;
      terminalWsUrl?: string;
    };
    return {
      environmentId: res.environmentId ?? '',
      status: mapStatus(res.status),
      editorUrl: endpoints.editorUrl,
      terminalWsUrl: endpoints.terminalWsUrl,
    };
  }

  async snapshotAndSuspend(input: {
    attemptId: string;
    environmentId: string;
  }): Promise<ProjectSnapshotHandle> {
    const res = await this.call<
      { environmentId: string; attemptId: string },
      { snapshotId?: string; manifest?: { capturedAt?: string } }
    >(
      'Snapshot',
      { environmentId: input.environmentId, attemptId: input.attemptId },
      120_000,
    );
    return {
      snapshotId: res.snapshotId ?? '',
      capturedAt: res.manifest?.capturedAt ?? new Date().toISOString(),
    };
  }

  async restore(input: {
    attemptId: string;
    snapshotId: string;
  }): Promise<ProjectEnvHandle> {
    const res = await this.call<
      { snapshotId: string; attemptId: string },
      { environmentId?: string; status?: string; endpoints?: unknown }
    >(
      'Restore',
      { snapshotId: input.snapshotId, attemptId: input.attemptId },
      120_000,
    );
    const endpoints = (res.endpoints ?? {}) as {
      editorUrl?: string;
      terminalWsUrl?: string;
    };
    return {
      environmentId: res.environmentId ?? '',
      status: mapStatus(res.status),
      editorUrl: endpoints.editorUrl,
      terminalWsUrl: endpoints.terminalWsUrl,
    };
  }

  async destroy(input: {
    attemptId: string;
    environmentId: string;
  }): Promise<{ alreadyDestroyed: boolean }> {
    const res = await this.call<
      { environmentId: string; attemptId: string; reason: string },
      { alreadyDestroyed?: boolean }
    >(
      'Destroy',
      {
        environmentId: input.environmentId,
        attemptId: input.attemptId,
        reason: 'submit',
      },
      60_000,
    );
    return { alreadyDestroyed: Boolean(res.alreadyDestroyed) };
  }
}

function mapStatus(s?: string): ProjectEnvHandle['status'] {
  switch (s) {
    case 'ENVIRONMENT_STATUS_READY':
      return 'READY';
    case 'ENVIRONMENT_STATUS_PROVISION_FAILED':
      return 'FAILED';
    default:
      return 'PROVISIONING';
  }
}
