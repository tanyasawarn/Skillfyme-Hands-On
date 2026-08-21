/**
 * Doc §8.1, §8.2, contracts/orchestrator.proto. This is the TypeScript-side
 * shape of the gRPC contract Dev A's Environment Orchestrator implements.
 * AttemptService depends on this interface, not a concrete gRPC client, so
 * swapping FakeOrchestratorClient for a real grpc-js client (once Dev A's
 * service exists) is the "swap-the-mock exercise" PLAN.md Phase 0 calls
 * for -- no AttemptService code changes, only the provider binding in
 * attempt.module.ts.
 */
export interface ProvisionRequest {
  attemptId: string;
  blueprintId: string;
  blueprintVersion: string;
  tier:
    | 'T0_BROWSER'
    | 'T1_SHARED_CONTAINER'
    | 'T2_ISOLATED_MICROVM'
    | 'T3_CLOUD_ACCOUNT';
  ttlMinutes: number;
  idleTimeoutMinutes: number;
}

export interface ProvisionResult {
  environmentId: string;
  status: 'PROVISIONING' | 'READY' | 'PROVISION_FAILED';
}

export interface DestroyRequest {
  environmentId: string;
  reason: 'submit' | 'idle' | 'ttl' | 'budget' | 'reaper' | 'admin';
}

export interface ConnectRequest {
  environmentId: string;
}

export interface ConnectResult {
  terminalWsUrl: string;
  editorUrl: string;
  sessionToken: string;
  expiresAt: string;
}

export interface ExecShellRequest {
  environmentId: string;
  command: string;
  timeoutMs?: number;
}

export interface ExecShellResult {
  exitCode: number;
  stdout: string;
  stderr: string;
  errorMessage?: string;
}

export const ORCHESTRATOR_CLIENT = Symbol('ORCHESTRATOR_CLIENT');

export interface OrchestratorClient {
  provision(req: ProvisionRequest): Promise<ProvisionResult>;
  destroy(req: DestroyRequest): Promise<{ alreadyDestroyed: boolean }>;
  connect(req: ConnectRequest): Promise<ConnectResult>;
  /**
   * Doc §8.5's Monaco editor is "client-side, file-API-backed" (no
   * server-side editor endpoint -- ConnectResult.editorUrl is always
   * "" for exactly that reason). This is that file API's transport: the
   * orchestrator's ExecShell RPC (contracts/orchestrator.proto,
   * built for the content-CI golden-path harness) runs an arbitrary
   * command inside the workspace pod and returns raw output -- the same
   * primitive, reused rather than adding a second bespoke RPC for what
   * is, underneath, just "run a shell command and get stdout back."
   */
  execShell(req: ExecShellRequest): Promise<ExecShellResult>;
}
