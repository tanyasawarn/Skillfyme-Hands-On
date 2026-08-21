import { Injectable, Logger, OnModuleInit } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import * as grpc from '@grpc/grpc-js';
import * as protoLoader from '@grpc/proto-loader';
import * as path from 'node:path';
import * as fs from 'node:fs';
import type {
  ValidatorExecutionResult,
  ValidatorExecutor,
  ValidatorSpec,
} from './validator-executor.interface';

/**
 * Real gRPC client against Dev A's Environment Orchestrator's
 * ExecValidator RPC -- same dynamic-proto-load pattern as
 * GrpcOrchestratorClient (attempt module), a second client instance
 * rather than sharing that one, since evaluation and attempt are
 * separate NestJS modules with no cross-dependency today.
 *
 * Only 6 of ValidatorSpec's 18 validator types have a real executor on
 * the orchestrator side (SHELL_ASSERT, SHELL_JSON, FILE_EXISTS,
 * FILE_CONTENT, FILE_PARSE, K8S_ASSERT -- see
 * orchestrator/internal/validation's package doc for why exactly these
 * six). A request for any other type gets back gRPC UNIMPLEMENTED,
 * which this class turns into a real ERROR result rather than throwing
 * -- doc §6.2: "ERROR (validator itself broke) is never scored against
 * the learner," so an unsupported type must flow through the normal
 * result path, not crash the whole validation run.
 */
@Injectable()
export class GrpcValidatorExecutor implements ValidatorExecutor, OnModuleInit {
  private readonly logger = new Logger(GrpcValidatorExecutor.name);
  private client!: any;

  constructor(private readonly config: ConfigService) {}

  onModuleInit() {
    const protoPath = this.resolveContractsPath('orchestrator.proto');
    const packageDefinition = protoLoader.loadSync(protoPath, {
      keepCase: false,
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
      `gRPC validator executor connecting to Environment Orchestrator at ${address}`,
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
            reject(err);
            return;
          }
          resolve(response);
        },
      );
    });
  }

  async execute(
    environmentId: string,
    spec: ValidatorSpec,
  ): Promise<ValidatorExecutionResult> {
    const start = Date.now();
    try {
      const response = await this.call<any, any>(
        'ExecValidator',
        {
          environmentId,
          validatorId: spec.id,
          validatorType: spec.type,
          run: spec.run ?? '',
          expectJson: JSON.stringify(spec.expect ?? {}),
          timeoutMs: spec.timeoutMs ?? 0,
        },
        (spec.timeoutMs ?? 30_000) + 5_000, // gRPC deadline padded beyond the validator's own timeout so the orchestrator's timeout fires first
      );

      let observed: unknown;
      try {
        observed = response.observedJson
          ? JSON.parse(response.observedJson)
          : undefined;
      } catch {
        observed = response.observedJson;
      }

      return {
        validatorId: spec.id,
        status:
          (response.status as ValidatorExecutionResult['status']) ?? 'ERROR',
        observed,
        expected: spec.expect,
        durationMs: response.durationMs ?? Date.now() - start,
        evidenceRef: response.errorMessage || undefined,
      };
    } catch (err) {
      // gRPC UNIMPLEMENTED (unsupported validator type) and any transport
      // failure both land here -- doc §6.2's ERROR contract applies to
      // both: never scored against the learner, but the caller needs a
      // result object, not a thrown exception, to record that.
      const grpcErr = err as grpc.ServiceError;
      this.logger.warn(
        `ExecValidator failed for ${spec.id} (${spec.type}) on env=${environmentId}: ${grpcErr.message ?? err}`,
      );
      return {
        validatorId: spec.id,
        status: 'ERROR',
        expected: spec.expect,
        durationMs: Date.now() - start,
        evidenceRef: grpcErr.message ?? String(err),
      };
    }
  }
}
