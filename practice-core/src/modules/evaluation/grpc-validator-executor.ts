import { Injectable, Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import { BaseGrpcClient } from '../../common/base-grpc-client';
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
export class GrpcValidatorExecutor
  extends BaseGrpcClient
  implements ValidatorExecutor
{
  protected readonly logger = new Logger(GrpcValidatorExecutor.name);
  protected readonly protoFile = 'orchestrator.proto';
  protected readonly protoServicePath =
    'practiceengine.orchestrator.v1.EnvironmentOrchestrator';

  constructor(config: ConfigService) {
    super(config);
  }

  protected connectionLogMessage(address: string): string {
    return `gRPC validator executor connecting to Environment Orchestrator at ${address}`;
  }

  async execute(
    environmentId: string,
    attemptId: string,
    spec: ValidatorSpec,
  ): Promise<ValidatorExecutionResult> {
    if (spec.type === 'NO_REGRESSION') {
      // CaptureBaseline/CheckRegression are deliberately NOT part of this
      // ownership-check pass (PLAN_RPC_AUTHZ.md Section 4d) -- they
      // weren't in the original 5-RPC access-control finding
      // (PHASE2_CLOSEOUT.md), and adding an ownership check to them is a
      // separately-scoped fix, not folded silently in here.
      return this.executeNoRegression(environmentId, spec);
    }

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
          attemptId,
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
      //
      // err here is a plain Error (BaseGrpcClient.call() always wraps a
      // gRPC failure into `new Error("gRPC <method> failed: <code>
      // <message>")`, not the raw grpc.ServiceError this file's own
      // catch block used to receive before the S3 base-class extraction)
      // -- .message is the only field this code ever read off it either
      // way, so the wrap changes no observable behavior, just the
      // accurate type.
      const message = err instanceof Error ? err.message : String(err);
      this.logger.warn(
        `ExecValidator failed for ${spec.id} (${spec.type}) on env=${environmentId}: ${message}`,
      );
      return {
        validatorId: spec.id,
        status: 'ERROR',
        expected: spec.expect,
        durationMs: Date.now() - start,
        evidenceRef: message,
      };
    }
  }

  /**
   * NO_REGRESSION doesn't go through ExecValidator (that RPC only backs
   * the 6 exec-in-pod/K8S_ASSERT types, see the class doc comment) --
   * it has its own two-phase orchestrator RPCs (CaptureBaseline before
   * the fault/change under test, CheckRegression to grade). By the time
   * a validator runs, capture already happened (or should have); this
   * method only ever calls the check half. Doc §7.3's own worked
   * example authors the snapshot_key in the validator's `run` field
   * (e.g. "baseline.other-services"), mirroring how SHELL_ASSERT's `run`
   * carries its command -- same field, per-type meaning.
   */
  private async executeNoRegression(
    environmentId: string,
    spec: ValidatorSpec,
  ): Promise<ValidatorExecutionResult> {
    const start = Date.now();
    const snapshotKey = spec.run;
    if (!snapshotKey) {
      // Content-authoring error, not a learner-caused failure -- doc
      // §6.2's ERROR contract: never scored against the learner, but
      // must still surface so the lint pipeline / content author sees it.
      return {
        validatorId: spec.id,
        status: 'ERROR',
        expected: spec.expect,
        durationMs: 0,
        evidenceRef:
          'NO_REGRESSION validator is missing its snapshot_key (run field)',
      };
    }

    try {
      const response = await this.call<any, any>(
        'CheckRegression',
        { environmentId, snapshotKey },
        (spec.timeoutMs ?? 30_000) + 5_000,
      );
      const regressionFound = Boolean(response.regressionFound);
      return {
        validatorId: spec.id,
        status: regressionFound ? 'FAIL' : 'PASS',
        observed: { regressed_resources: response.regressedResources ?? [] },
        expected: spec.expect,
        durationMs: Date.now() - start,
      };
    } catch (err) {
      // Most common cause: CheckRegression before any CaptureBaseline
      // ever ran for this environment+snapshot_key (gRPC NOT_FOUND) --
      // a platform/sequencing gap, not a learner mistake, so ERROR per
      // doc §6.2, same as an unsupported validator type. See execute()'s
      // own catch block for why err is a plain Error here, not
      // grpc.ServiceError (BaseGrpcClient.call() always wraps).
      const message = err instanceof Error ? err.message : String(err);
      this.logger.warn(
        `CheckRegression failed for ${spec.id} (snapshot_key=${snapshotKey}) on env=${environmentId}: ${message}`,
      );
      return {
        validatorId: spec.id,
        status: 'ERROR',
        expected: spec.expect,
        durationMs: Date.now() - start,
        evidenceRef: message,
      };
    }
  }

  /**
   * Doc §7.3 step 3: captures the environment's health matrix as a
   * named baseline, to be diffed later by a NO_REGRESSION validator's
   * CheckRegression call. Not invoked from ValidatorExecutor.execute()
   * (baseline capture happens once, right after the health gate and
   * before any fault is injected -- an attempt-lifecycle sequencing
   * point, not a per-validator-run one); exposed here as a public
   * method for that lifecycle code to call once it exists (PLAN.md
   * Phase 2 fault-injection trigger wiring).
   */
  async captureBaseline(
    environmentId: string,
    snapshotKey: string,
  ): Promise<{
    snapshotKey: string;
    capturedAt: string;
    resourcesCaptured: number;
  }> {
    const response = await this.call<any, any>('CaptureBaseline', {
      environmentId,
      snapshotKey,
    });
    return {
      snapshotKey: response.snapshotKey,
      capturedAt: response.capturedAt,
      resourcesCaptured: response.resourcesCaptured ?? 0,
    };
  }
}
