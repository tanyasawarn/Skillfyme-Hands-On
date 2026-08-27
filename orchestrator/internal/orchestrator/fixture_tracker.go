package orchestrator

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/fixture"
)

// PostgresFixtureTracker implements internal/fixture.AppliedTracker
// against env.fixture_applied (db/migrations/0003_fixture_applied.sql).
// A thin adapter, not fixture package's own concern, per that package's
// doc comment ("this package doesn't own persistence").
type PostgresFixtureTracker struct {
	db *pgxpool.Pool
}

func NewPostgresFixtureTracker(db *pgxpool.Pool) *PostgresFixtureTracker {
	return &PostgresFixtureTracker{db: db}
}

func (t *PostgresFixtureTracker) IsApplied(ctx context.Context, envID, fixtureID string, checksum fixture.Checksum) (bool, error) {
	var found string
	err := t.db.QueryRow(ctx,
		`SELECT checksum FROM env.fixture_applied WHERE environment_id = $1 AND fixture_id = $2`,
		envID, fixtureID,
	).Scan(&found)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	// Doc §5.5 step 3's "checksummed": a row exists but at a DIFFERENT
	// checksum means the fixture's implementation changed since it was
	// last applied to this environment -- must re-apply, not skip.
	return found == checksum, nil
}

func (t *PostgresFixtureTracker) MarkApplied(ctx context.Context, envID, fixtureID string, checksum fixture.Checksum) error {
	_, err := t.db.Exec(ctx,
		`INSERT INTO env.fixture_applied (environment_id, fixture_id, checksum)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (environment_id, fixture_id) DO UPDATE SET checksum = $3, applied_at = now()`,
		envID, fixtureID, checksum,
	)
	return err
}
