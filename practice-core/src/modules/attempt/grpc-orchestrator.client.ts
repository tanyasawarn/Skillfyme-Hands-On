import { Injectable, Logger, OnModuleInit } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import * as grpc from '@grpc/grpc-js';
import * as protoLoader from '@grpc/proto-loader';
import * as path from 'node:path';
import * as fs from 'node:fs';
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
  implements OrchestratorClient, OnModuleInit
{
  private readonly logger = new Logger(GrpcOrchestratorClient.name);
  private client!: any;

  constructor(private readonly config: ConfigService) {}

  onModuleInit() {
    const protoPath = this.resolveContractsPath('orchestrator.proto');
    const packageDefinition = protoLoader.loadSync(protoPath, {
      keepCase: false, // camelCase field names on the JS side, matching orchestrator-client.interface.ts
      longs: String,
      enums: String,
      defaults: true,
      oneofs: true,
    });
    const proto = grpc.loadPackageDefinition(packageDefinition) as any;
    const ServiceCtor =
      proto.practiceengine.orchestrator.v1.EnvironmentOrchestrator;

    const address =
      this.config.get<string>('ORCHESTRATOR_GRPC_ADDRESS') ?? 'localhost:50051';
    this.client = new ServiceCtor(address, grpc.credentials.createInsecure());
    this.logger.log(
      `gRPC client connecting to Environment Orchestrator at ${address}`,
    );
  }

  private resolveContractsPath(file: string): string {
    const fromDirname = path.resolve(__dirname, '../../../../contracts', file);
    if (fs.existsSync(fromDirname)) return fromDirname;
    const fromCwd = path.resolve(process.cwd(), '../contracts', file);
    if (fs.existsSync(fromCwd)) return fromCwd;
    throw new Error(
      `contracts/${file} not found from ${fromDirname} or ${fromCwd}`,
    );
  }

  private call<TReq, TRes>(
    method: string,
    request: TReq,
    deadlineMs = 30_000,
  ): Promise<TRes> {
    return new Promise((resolve, reject) => {
      const deadline = new Date(Date.now() + deadlineMs);
      this.client[method](
        request,
        { deadline },
        (err: grpc.ServiceError | null, response: TRes) => {
          if (err) {
            reject(
              new Error(`gRPC ${method} failed: ${err.code} ${err.message}`),
            );
            return;
          }
          resolve(response);
        },
      );
    });
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
    });
    return { alreadyDestroyed: !!response.alreadyDestroyed };
  }

  async connect(req: ConnectRequest): Promise<ConnectResult> {
    const response = await this.call<any, any>('Connect', {
      environmentId: req.environmentId,
    });
    return {
      terminalWsUrl: response.terminalWsUrl,
      editorUrl: response.editorUrl,
      sessionToken: response.sessionToken,
      expiresAt: response.expiresAt,
    };
  }

  async execShell(req: ExecShellRequest): Promise<ExecShellResult> {
    const response = await this.call<any, any>(
      'ExecShell',
      {
        environmentId: req.environmentId,
        command: req.command,
        timeoutMs: req.timeoutMs ?? 0,
      },
      (req.timeoutMs ?? 30_000) + 5_000,
    );
    return {
      exitCode: Number(response.exitCode ?? 0),
      stdout: response.stdout ?? '',
      stderr: response.stderr ?? '',
      errorMessage: response.errorMessage || undefined,
    };
  }
}
