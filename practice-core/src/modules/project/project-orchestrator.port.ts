/**
 * Phase 3 (PLAN_PHASE3_PROJECTS.md 1.6 / B2, IP-B2). The narrow slice of
 * the Environment Orchestrator contract the milestone state machine
 * needs: provision a T3 environment for a milestone that requires one,
 * snapshot-on-idle when the learner steps away, and destroy on
 * completion.
 *
 * A dedicated port (not the attempt module's OrchestratorClient) because
 * the project module is built and tested against a fake until the real
 * T3 driver lands (Stage 3.2 swaps FakeProjectOrchestrator for a real
 * adapter — "swap Dev B's fake for the real driver here", 3.4). The real
 * adapter will translate these calls into Provision(tier=T3) /
 * Snapshot / Destroy on contracts/orchestrator.proto.
 */

export interface ProjectEnvHandle {
  environmentId: string;
  status: 'PROVISIONING' | 'READY' | 'FAILED';
  /** OpenVSCode / terminal URLs, populated when READY. */
  editorUrl?: string;
  terminalWsUrl?: string;
}

export interface ProjectSnapshotHandle {
  snapshotId: string;
  capturedAt: string;
}

export const PROJECT_ORCHESTRATOR = Symbol('PROJECT_ORCHESTRATOR');

export interface ProjectOrchestratorPort {
  /**
   * Provision a T3 cloud-account environment for a milestone. `milestoneKey`
   * is passed for correlation / logging; the account claim + baseline
   * apply happen orchestrator-side.
   */
  provisionForMilestone(input: {
    attemptId: string;
    milestoneKey: string;
  }): Promise<ProjectEnvHandle>;

  /**
   * Snapshot IaC + inventory state and tear down the compute (the "norm"
   * for project mode — §12.3). The environment can be Restored later.
   */
  snapshotAndSuspend(input: {
    attemptId: string;
    environmentId: string;
  }): Promise<ProjectSnapshotHandle>;

  /** Restore a suspended environment from its snapshot. */
  restore(input: {
    attemptId: string;
    snapshotId: string;
  }): Promise<ProjectEnvHandle>;

  /** Final teardown (release the cloud account). */
  destroy(input: {
    attemptId: string;
    environmentId: string;
  }): Promise<{ alreadyDestroyed: boolean }>;
}
