package accountpool

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ReclaimForAttempt is the Restore-path counterpart of Claim
// (snapshotstate.AccountReclaimer). A resumed T3 project attempt wants
// its ORIGINAL sandbox account back if it is still pooled -- the
// baseline Terraform is already applied on it and its remote state is
// intact, so re-using it skips a fresh baseline apply. If the preferred
// account is not currently AVAILABLE (it got claimed by someone else, or
// swept), fall back to a normal Claim of any AVAILABLE account in the
// region.
//
// Returns the account id + the role the credential broker assumes into.
//
// Preconditions: the caller has already stopped any prior broker for
// this attempt. Idempotent enough for a retried Restore: a second call
// that finds the preferred account already IN_USE *by this same attempt*
// returns it without re-running the claim side effects.
func (m *Manager) ReclaimForAttempt(
	ctx context.Context,
	attemptID, tenantID, region, preferredAccountID string,
	budgetUSD float64,
) (accountID, roleName string, err error) {
	if preferredAccountID != "" {
		// Fast path: is the preferred account already IN_USE by THIS
		// attempt (retried Restore)? Then just hand it back.
		var state string
		var holder *string
		qerr := m.db.QueryRow(ctx,
			`SELECT state, attempt_id FROM env.cloud_account WHERE aws_account_id = $1`,
			preferredAccountID).Scan(&state, &holder)
		if qerr == nil && state == "IN_USE" && holder != nil && *holder == attemptID {
			return preferredAccountID, roleForReclaim(), nil
		}

		// Try to grab the preferred account specifically: AVAILABLE →
		// IN_USE, scoped to this exact id. If it isn't AVAILABLE this
		// affects 0 rows and we fall through to a generic claim.
		tag, uerr := m.db.Exec(ctx, `
			UPDATE env.cloud_account
			   SET state = 'IN_USE',
			       attempt_id = $2,
			       budget_usd = $3,
			       claimed_at = now(),
			       released_at = NULL,
			       quarantine_reason = NULL,
			       quarantine_resources_remaining = NULL,
			       quarantine_detail = NULL,
			       updated_at = now()
			 WHERE aws_account_id = $1 AND state = 'AVAILABLE'`,
			preferredAccountID, attemptID, budgetUSD)
		if uerr != nil {
			return "", "", fmt.Errorf("accountpool: reclaim preferred: %w", uerr)
		}
		if tag.RowsAffected() == 1 {
			// Pull it out of the Redis AVAILABLE set so a concurrent
			// Claim can't also hand it out.
			var reg string
			_ = m.db.QueryRow(ctx,
				`SELECT region FROM env.cloud_account WHERE aws_account_id = $1`,
				preferredAccountID).Scan(&reg)
			if reg != "" {
				_ = m.rdb.SRem(ctx, availKey(reg), preferredAccountID).Err()
			}
			// Re-arm the budget alarm + SKU tag; baseline is already
			// applied on this account, so skip ApplyBaseline.
			if berr := m.aws.PutAccountBudget(ctx, preferredAccountID, budgetUSD, m.budgetThresholds); berr != nil {
				m.rollbackClaim(ctx, preferredAccountID, "PutAccountBudget(reclaim)", berr)
				return "", "", berr
			}
			m.events.PublishAccountClaimed(ctx, attemptID, preferredAccountID, region)
			log.Info("sandbox account reclaimed for resumed attempt",
				"account_id", preferredAccountID, "attempt_id", attemptID)
			return preferredAccountID, roleForReclaim(), nil
		}
	}

	// Fall back to a fresh claim of any AVAILABLE account in the region.
	res, cerr := m.Claim(ctx, ClaimInput{
		AttemptID: attemptID,
		TenantID:  tenantID,
		Region:    region,
		BudgetUSD: budgetUSD,
	})
	if cerr != nil {
		return "", "", fmt.Errorf("accountpool: reclaim fallback claim: %w", cerr)
	}
	return res.AccountID, res.RoleName, nil
}

// roleForReclaim is the sandbox role name a reclaimed account exposes.
// The baseline module always names it the same (LearnerSandboxRole), so
// a reclaim -- which skips ApplyBaseline -- can return it without a
// round-trip. Kept as a function (not a bare const at the call sites) so
// a future config-driven role name has one place to change.
func roleForReclaim() string { return "LearnerSandboxRole" }

// ensure pgx import is used even if the fast-path query above is edited
// out later.
var _ = pgx.ErrNoRows
