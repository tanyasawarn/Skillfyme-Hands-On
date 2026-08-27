-- PLAN.md M1.14 "Security baseline audit": doc's own security-baseline
-- checklist names "audit log" alongside gVisor/NetworkPolicy/egress-
-- proxy/quotas/reaper -- all five of those were real before this
-- migration; audit log was the one still missing (the only "audit"
-- artifact that existed was a K8s PodSecurity admission-controller
-- label, which records nothing about WHO did WHAT WHEN -- an admission
-- decision, not an audit trail).
--
-- Scope: every security-relevant RPC this orchestrator serves --
-- Provision, Destroy, InjectFault, MintValidatorCredentials, ExecShell
-- (arbitrary command execution) -- gets a row here. Doc's own T12 threat
-- ("audit logging of every admin read of learner data") is a Phase 3+
-- admin-specific concern layered on top of this; this table is the
-- Phase 1 baseline: what happened to which environment, when, and
-- (where known) at whose request.

CREATE TABLE IF NOT EXISTS env.audit_log (
  id             uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  occurred_at    timestamptz NOT NULL DEFAULT now(),
  environment_id text,
  attempt_id     text,
  action         text NOT NULL,          -- e.g. "PROVISION", "DESTROY", "INJECT_FAULT", "MINT_CREDENTIALS", "EXEC_SHELL"
  outcome        text NOT NULL,          -- "SUCCESS" or "FAILURE"
  detail         jsonb NOT NULL DEFAULT '{}'::jsonb,  -- action-specific, non-secret detail (e.g. fault_id, reason) -- NEVER raw tokens/command output, see internal/audit's own doc comment
  error_message  text
);

-- Doc's own hot-query framing (reaper table's index comment, same
-- convention applied here): the two real access patterns are "everything
-- for one environment" (incident investigation) and "everything in a
-- time range" (periodic review) -- both need to avoid a seq-scan on a
-- table that grows without bound.
CREATE INDEX IF NOT EXISTS idx_audit_log_environment ON env.audit_log (environment_id, occurred_at);
CREATE INDEX IF NOT EXISTS idx_audit_log_occurred_at ON env.audit_log (occurred_at);
