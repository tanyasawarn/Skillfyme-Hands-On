/**
 * Doc §8.1, §8.2, contracts/orchestrator.proto. This is the TypeScript-side
 * shape of the gRPC contract Dev A's Environment Orchestrator implements.
 * AttemptService depends on this interface, not a concrete gRPC client, so
 * swapping FakeOrchestratorClient for a real grpc-js client (once Dev A's
 * service exists) is the "swap-the-mock exercise" PLAN.md Phase 0 calls
 * for -- no AttemptService code changes, only the provider binding in
 * attempt.module.ts.
 */
export interface FixtureRef {
  fixtureId: string;
  version: string;
}

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
  // PLAN.md M1.3: the activity's ordered environment.seed fixture list.
  // Optional (defaults to empty) so existing callers/tests that don't
  // care about fixtures don't all need updating.
  fixtures?: FixtureRef[];
  // PLAN.md M1.4/M1.14: the activity's health_gate array (contracts/
  // activity_spec.schema.json), JSON-stringified before crossing the
  // wire (ProvisionRequest.health_gate_json is a string field, matching
  // InjectFaultRequest.params' "plain string, richer shape parsed by
  // the receiver" precedent). Optional/undefined means "no richer gate
  // declared" -- pod-Ready remains the whole gate, unchanged Phase 1
  // behavior for any activity that doesn't opt in.
  healthGateJson?: string;
}

export interface ProvisionResult {
  environmentId: string;
  status: 'PROVISIONING' | 'READY' | 'PROVISION_FAILED';
}

export interface DestroyRequest {
  environmentId: string;
  reason: 'submit' | 'idle' | 'ttl' | 'budget' | 'reaper' | 'admin';
  // PHASE2_CLOSEOUT.md's flagged access-control gap, closed for this RPC
  // too (originally only InjectFault had this check): the orchestrator
  // verifies this matches env.environment.attempt_id for environmentId
  // before tearing it down (PermissionDenied on mismatch), EXCEPT when
  // environmentId doesn't exist at all (already-destroyed is a no-op
  // success requiring no attemptId) -- see orchestrator/internal/orchestrator/server.go's
  // Destroy. Required on any environment that still exists.
  attemptId: string;
}

export interface ConnectRequest {
  environmentId: string;
  // Ownership check, same pattern as InjectFaultRequest.attemptId above:
  // the orchestrator verifies this matches env.environment.attempt_id
  // for environmentId before minting a session token. Required.
  attemptId: string;
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
  // Ownership check, same pattern as InjectFaultRequest.attemptId above:
  // the orchestrator verifies this matches env.environment.attempt_id
  // for environmentId before running the command. Required.
  attemptId: string;
}

export interface ExecShellResult {
  exitCode: number;
  stdout: string;
  stderr: string;
  errorMessage?: string;
}

/**
 * Doc §3.2/§7.3, PLAN.md Phase 2 integration point: "Fault application is
 * triggered by Dev B's Attempt Service but executed by Dev A's
 * Orchestrator (InjectFault(env_id, fault_spec) RPC)." Params mirror a
 * fault's authored params_schema (content/faults/<id>.yaml) as plain
 * string key/values -- the orchestrator's handler registry
 * (orchestrator/internal/faultinjection) already expects exactly that
 * shape (map[string]string), so no richer typing is introduced here.
 */
export interface InjectFaultRequest {
  environmentId: string;
  faultId: string;
  params: Record<string, string>;
  // PHASE2_CLOSEOUT.md's flagged access-control gap, closed: the
  // orchestrator verifies this matches env.environment.attempt_id for
  // environmentId before applying anything (PermissionDenied on
  // mismatch) -- see orchestrator/internal/orchestrator/server.go's
  // InjectFault. Required, same as environmentId/faultId.
  attemptId: string;
}

export interface InjectFaultResult {
  applied: boolean;
  symptomVerified: boolean;
}

export const ORCHESTRATOR_CLIENT = Symbol('ORCHESTRATOR_CLIENT');

export interface OrchestratorClient {
  provision(req: ProvisionRequest): Promise<ProvisionResult>;
  destroy(req: DestroyRequest): Promise<{ alreadyDestroyed: boolean }>;
  connect(req: ConnectRequest): Promise<ConnectResult>;
  injectFault(req: InjectFaultRequest): Promise<InjectFaultResult>;
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
