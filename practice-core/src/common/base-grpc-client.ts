import { Logger, OnModuleInit } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import * as fs from 'node:fs';
import * as grpc from '@grpc/grpc-js';
import * as protoLoader from '@grpc/proto-loader';
import { resolveRepoRelativePath } from './repo-relative-path';
import { GrpcClientConstants } from './constants';

/**
 * PLAN.md Phase 3's S3: proto-loading, address resolution, and the
 * promisified call() helper were duplicated wholesale between
 * GrpcOrchestratorClient (attempt module) and GrpcValidatorExecutor
 * (evaluation module) -- both real gRPC clients against the same
 * orchestrator.proto contract, kept as two separate client instances by
 * design (different NestJS modules, no cross-dependency), but the
 * connection plumbing itself had no reason to be reimplemented twice.
 *
 * Abstract base rather than a standalone helper function: both real
 * subclasses need NestJS's OnModuleInit lifecycle (the proto load and
 * gRPC channel creation happen at module-init time, not construction
 * time) and constructor-injected ConfigService, which a base class
 * models more directly than a free function each subclass would need to
 * call manually from its own onModuleInit anyway.
 *
 * error handling: call() always wraps a failure into a real Error with a
 * formatted "gRPC <method> failed: <code> <message>" string (matching
 * GrpcOrchestratorClient's original behavior) -- confirmed before this
 * extraction that GrpcValidatorExecutor's own catch block (which
 * originally received the raw grpc.ServiceError instead) only ever reads
 * `.message` off the caught error, never `.code` or any other
 * ServiceError-specific field, so unifying to always-wrap changes no
 * observable behavior for either subclass.
 */
export abstract class BaseGrpcClient implements OnModuleInit {
  protected abstract readonly logger: Logger;
  protected client!: any;
  private sharedSecret: string | undefined;

  constructor(protected readonly config: ConfigService) {}

  /**
   * protoServicePath is the dotted path into the loaded proto's package
   * definition (e.g. "practiceengine.orchestrator.v1.EnvironmentOrchestrator")
   * -- both real subclasses target the same service today, but this
   * stays a subclass-supplied value rather than hardcoded, since a
   * future gRPC client added via this base could target a different
   * proto file/service entirely.
   */
  protected abstract readonly protoFile: string;
  protected abstract readonly protoServicePath: string;

  /** Log line subclasses want on successful connection -- kept subclass-controlled since GrpcOrchestratorClient and GrpcValidatorExecutor use distinct wording ("gRPC client" vs "gRPC validator executor"). */
  protected abstract connectionLogMessage(address: string): string;

  onModuleInit() {
    const protoPath = this.resolveContractsPath(this.protoFile);
    const packageDefinition = protoLoader.loadSync(protoPath, {
      keepCase: false, // camelCase field names on the JS side
      longs: String,
      enums: String,
      defaults: true,
      oneofs: true,
    });
    const proto = grpc.loadPackageDefinition(packageDefinition) as any;
    const ServiceCtor = this.protoServicePath
      .split('.')
      .reduce((obj, key) => obj[key], proto);

    const address =
      this.config.get<string>('ORCHESTRATOR_GRPC_ADDRESS') ?? 'localhost:50051';
    this.client = new ServiceCtor(address, this.buildChannelCredentials());

    // Matches internal/orchestrator/auth.go's shared-secret bearer-token
    // interceptor: when the orchestrator has ORCHESTRATOR_SHARED_SECRET
    // set, every RPC (except its own health check) requires an
    // "authorization: Bearer <token>" metadata header. Reading the same
    // env var name on this side keeps the two services' config in sync
    // without a separate contract.
    this.sharedSecret = this.config.get<string>('ORCHESTRATOR_SHARED_SECRET');
    this.onSharedSecretResolved(this.sharedSecret);

    this.logger.log(this.connectionLogMessage(address));
  }

  /**
   * Hook for subclass-specific behavior when sharedSecret resolves --
   * GrpcOrchestratorClient warns loudly when it's unset (the ORIGINAL
   * asymmetry this extraction preserves rather than silently unifies:
   * GrpcValidatorExecutor never had this warning, and unifying it would
   * be a real behavior change -- doubling a warning that already fires
   * once per process, from the other client, isn't a correctness issue,
   * but changing which subclasses warn is a decision belonging to
   * whoever owns that operational signal, not something this refactor
   * should decide unilaterally). Default no-op so a subclass that
   * doesn't override it (GrpcValidatorExecutor) keeps its original
   * silence.
   */
  protected onSharedSecretResolved(secret: string | undefined): void {
    // no-op by default
  }

  private resolveContractsPath(file: string): string {
    return resolveRepoRelativePath(__dirname, 4, `contracts/${file}`);
  }

  /**
   * mTLS (PLAN.md Phase 2 closure item: plaintext gRPC was a named
   * security gap -- see internal/orchestrator/mtls.go on the Go side for
   * the server-side counterpart). ORCHESTRATOR_TLS_CA is the flag this
   * side keys off: if it's set, this client presents
   * ORCHESTRATOR_CLIENT_TLS_CERT/_KEY as its own identity and verifies
   * the server's certificate against that CA -- matching the server's
   * tls.RequireAndVerifyClientCert, a connection with no client cert (or
   * one this CA didn't sign) never completes the handshake.
   *
   * Fails loud (throws at module-init time, before any RPC is attempted)
   * if TLS was requested but any of the three files can't be read --
   * same "never silently fall back to plaintext once configured" stance
   * as the orchestrator's own config.TLSEnabled. If ORCHESTRATOR_TLS_CA
   * is unset, this returns insecure credentials unchanged (the original
   * behavior) -- local dev without mTLS configured keeps working exactly
   * as before.
   */
  private buildChannelCredentials(): grpc.ChannelCredentials {
    const caPath = this.config.get<string>('ORCHESTRATOR_TLS_CA');
    if (!caPath) {
      return grpc.credentials.createInsecure();
    }

    const certPath = this.config.get<string>('ORCHESTRATOR_CLIENT_TLS_CERT');
    const keyPath = this.config.get<string>('ORCHESTRATOR_CLIENT_TLS_KEY');
    if (!certPath || !keyPath) {
      throw new Error(
        'ORCHESTRATOR_TLS_CA is set but ORCHESTRATOR_CLIENT_TLS_CERT/_KEY are not -- mTLS requires all three, refusing to fall back to plaintext or one-way TLS',
      );
    }

    let ca: Buffer, cert: Buffer, key: Buffer;
    try {
      ca = fs.readFileSync(caPath);
      cert = fs.readFileSync(certPath);
      key = fs.readFileSync(keyPath);
    } catch (e) {
      throw new Error(
        `mTLS is configured (ORCHESTRATOR_TLS_CA is set) but a cert file could not be read: ${(e as Error).message}`,
      );
    }

    return grpc.credentials.createSsl(ca, key, cert);
  }

  protected call<TReq, TRes>(
    method: string,
    request: TReq,
    deadlineMs: number = GrpcClientConstants.DEFAULT_DEADLINE_MS,
  ): Promise<TRes> {
    return new Promise((resolve, reject) => {
      const deadline = new Date(Date.now() + deadlineMs);
      const metadata = new grpc.Metadata();
      if (this.sharedSecret) {
        metadata.set('authorization', `Bearer ${this.sharedSecret}`);
      }
      this.client[method](
        request,
        metadata,
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
}
