import { Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { BaseGrpcClient } from '../../common/base-grpc-client';
import { GrpcClientConstants } from '../../common/constants';
import type {
  ConnectRequest,
  ConnectResult,
  DestroyRequest,
  ExecShellRequest,
  ExecShellResult,
  InjectFaultRequest,
  InjectFaultResult,
  OrchestratorClient,
  ProvisionRequest,
  ProvisionResult,
} from './orchestrator-client.interface';

/**
 * Real gRPC client against Dev A's Environment Orchestrator, implementing
 * contracts/orchestrator.proto directly (dynamic proto loading via
 * @grpc/proto-loader, not codegen -- avoids needing a TS codegen step
 * kept in sync with the .proto file separately from Dev A's Go codegen).
 * This is the "swap-the-mock exercise" PLAN.md Phase 0 promised:
 * AttemptService/attempt.module.ts's ORCHESTRATOR_CLIENT binding is the
 * only thing that changes to go from FakeOrchestratorClient to this.
 */
@Injectable()
export class GrpcOrchestratorClient
  extends BaseGrpcClient
  implements OrchestratorClient
{
  protected readonly logger = new Logger(GrpcOrchestratorClient.name);
  protected readonly protoFile = 'orchestrator.proto';
  protected readonly protoServicePath =
    'practiceengine.orchestrator.v1.EnvironmentOrchestrator';

  constructor(config: ConfigService) {
    super(config);
  }

  protected connectionLogMessage(address: string): string {
    return `gRPC client connecting to Environment Orchestrator at ${address}`;
  }

  // Preserves the original asymmetry: only this client (not
  // GrpcValidatorExecutor) ever warned when ORCHESTRATOR_SHARED_SECRET
  // is unset -- see base-grpc-client.ts's own doc comment on why that
  // asymmetry is kept rather than silently unified.
  protected onSharedSecretResolved(secret: string | undefined): void {
    if (!secret) {
      this.logger.warn(
        'ORCHESTRATOR_SHARED_SECRET is not set -- calls to the Environment Orchestrator will carry no auth token. If the orchestrator has its own ORCHESTRATOR_SHARED_SECRET set, every call will be rejected as Unauthenticated.',
      );
    }
  }

  // doc §5.1 tier naming vs. this service's OrchestratorClient interface
  // naming -- see orchestrator-client.interface.ts's ProvisionRequest.tier.
  private static readonly TIER_MAP: Record<ProvisionRequest['tier'], string> = {
    T0_BROWSER: 'TIER_T0_BROWSER',
    T1_SHARED_CONTAINER: 'TIER_T1_SHARED_CONTAINER',
    T2_ISOLATED_MICROVM: 'TIER_T2_ISOLATED_MICROVM',
    T3_CLOUD_ACCOUNT: 'TIER_T3_CLOUD_ACCOUNT',
  };

  async provision(req: ProvisionRequest): Promise<ProvisionResult> {
    const response = await this.call<any, any>(
      'Provision',
      {
        attemptId: req.attemptId,
        blueprintId: req.blueprintId,
        blueprintVersion: req.blueprintVersion,
        tier: GrpcOrchestratorClient.TIER_MAP[req.tier],
        ttlMinutes: req.ttlMinutes,
        idleTimeoutMinutes: req.idleTimeoutMinutes,
        fixtures: (req.fixtures ?? []).map((f) => ({
          fixtureId: f.fixtureId,
          version: f.version,
        })),
        healthGateJson: req.healthGateJson ?? '',
      },
      60_000, // provisioning blocks on the health gate server-side; needs headroom beyond the default
    );

    const statusMap: Record<string, ProvisionResult['status']> = {
      ENVIRONMENT_STATUS_PROVISIONING: 'PROVISIONING',
      ENVIRONMENT_STATUS_READY: 'READY',
      ENVIRONMENT_STATUS_PROVISION_FAILED: 'PROVISION_FAILED',
    };

    return {
      environmentId: response.environmentId,
      status: statusMap[response.status] ?? 'PROVISION_FAILED',
    };
  }

  async destroy(req: DestroyRequest): Promise<{ alreadyDestroyed: boolean }> {
    const response = await this.call<any, any>('Destroy', {
      environmentId: req.environmentId,
      reason: req.reason,
      attemptId: req.attemptId,
    });
    return { alreadyDestroyed: !!response.alreadyDestroyed };
  }

  async connect(req: ConnectRequest): Promise<ConnectResult> {
    const response = await this.call<any, any>('Connect', {
      environmentId: req.environmentId,
      attemptId: req.attemptId,
    });
    return {
      terminalWsUrl: response.terminalWsUrl,
      editorUrl: response.editorUrl,
      sessionToken: response.sessionToken,
      expiresAt: response.expiresAt,
    };
  }

  /**
   * PLAN.md Phase 2 integration point: "Fault application is triggered
   * by Dev B's Attempt Service but executed by Dev A's Orchestrator."
   * The orchestrator distinguishes two "can't apply" cases at the gRPC
   * code level (see orchestrator/internal/orchestrator/server.go's
   * InjectFault): UNIMPLEMENTED for a fault_id with no registered
   * handler at all, FAILED_PRECONDITION for one that's registered but
   * whose execution mechanism is deferred (no baseline fixture, tier
   * unavailable, or a specific pending contract -- see
   * faultinjection.ErrUnsupportedMechanism's reason tags). Both, like
   * any other RPC failure here, collapse to applied=false rather than a
   * thrown exception, matching GrpcValidatorExecutor's ERROR-not-throw
   * contract for the same class of "content references something the
   * platform can't do yet" gap -- the caller (AttemptService) needs a
   * result to log/record, not a crashed provision() call. The FAILED_
   * PRECONDITION reason tag is still visible in the warn log below if
   * deeper triage is needed; it isn't surfaced structurally on
   * InjectFaultResult because InjectFaultResponse (contracts/) has no
   * field for it yet -- adding one is a joint-review contract change,
   * not a client-side concern.
   */
  async injectFault(req: InjectFaultRequest): Promise<InjectFaultResult> {
    try {
      const response = await this.call<any, any>('InjectFault', {
        environmentId: req.environmentId,
        faultId: req.faultId,
        params: req.params,
        attemptId: req.attemptId,
      });
      return {
        applied: !!response.applied,
        symptomVerified: !!response.symptomVerified,
      };
    } catch (err) {
      this.logger.warn(
        `InjectFault failed for fault=${req.faultId} on env=${req.environmentId}: ${err instanceof Error ? err.message : err}`,
      );
      return { applied: false, symptomVerified: false };
    }
  }

  async execShell(req: ExecShellRequest): Promise<ExecShellResult> {
    const response = await this.call<any, any>(
      'ExecShell',
      {
        environmentId: req.environmentId,
        command: req.command,
        timeoutMs: req.timeoutMs ?? 0,
        attemptId: req.attemptId,
      },
      (req.timeoutMs ?? GrpcClientConstants.DEFAULT_DEADLINE_MS) + 5_000,
    );
    return {
      exitCode: Number(response.exitCode ?? 0),
      stdout: response.stdout ?? '',
      stderr: response.stderr ?? '',
      errorMessage: response.errorMessage || undefined,
    };
  }
}
