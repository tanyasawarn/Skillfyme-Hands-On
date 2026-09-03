package cloudnuke

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/testsupport"
)

type recorderPager struct {
	mu    sync.Mutex
	pages []string
}

func (p *recorderPager) Page(_ context.Context, accountID, reason, detail string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pages = append(p.pages, accountID+"/"+reason)
}

func newTestSweeper(t *testing.T) (*Sweeper, *cloudaws.FakeClient, *recorderPager) {
	t.Helper()
	db := testsupport.NewPostgres(t)
	fake := cloudaws.NewFakeClient()
	pager := &recorderPager{}
	return NewSweeper(db, fake, pager), fake, pager
}

func seed(t *testing.T, s *Sweeper, id, state string) {
	t.Helper()
	_, err := s.db.Exec(context.Background(),
		`INSERT INTO env.cloud_account (aws_account_id, state, region) VALUES ($1, $2, 'us-east-1')
		 ON CONFLICT (aws_account_id) DO UPDATE SET state = $2`, id, state)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func stateOf(t *testing.T, s *Sweeper, id string) string {
	t.Helper()
	var st string
	if err := s.db.QueryRow(context.Background(),
		`SELECT state FROM env.cloud_account WHERE aws_account_id=$1`, id).Scan(&st); err != nil {
		t.Fatalf("stateOf: %v", err)
	}
	return st
}

func TestSweep_CleanAvailableStaysAvailable(t *testing.T) {
	s, fake, pager := newTestSweeper(t)
	seed(t, s, "111111111111", "AVAILABLE")
	fake.NukeFn = func(string) (cloudaws.NukeResult, error) {
		return cloudaws.NukeResult{Verified: true}, nil
	}
	res := s.sweepOnce(context.Background())
	if res.Checked != 1 || res.CleanReconfirmed != 1 || res.Quarantined != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	if stateOf(t, s, "111111111111") != "AVAILABLE" {
		t.Errorf("clean AVAILABLE account should stay AVAILABLE")
	}
	if len(pager.pages) != 0 {
		t.Errorf("no page expected for a clean sweep, got %v", pager.pages)
	}
}

func TestSweep_LeakInAvailableAccountQuarantinesAndPages(t *testing.T) {
	s, fake, pager := newTestSweeper(t)
	seed(t, s, "222222222222", "AVAILABLE")
	fake.NukeFn = func(string) (cloudaws.NukeResult, error) {
		return cloudaws.NukeResult{
			Verified: false, ResourcesRemaining: 2,
			BlindSpotHits: []string{"route53:hostedzone/Zabc"},
			Detail:        "leftover NAT gateway",
		}, nil
	}
	res := s.sweepOnce(context.Background())
	if res.Quarantined != 1 {
		t.Fatalf("expected 1 quarantine, got %+v", res)
	}
	if stateOf(t, s, "222222222222") != "QUARANTINED" {
		t.Errorf("leaking AVAILABLE account must be QUARANTINED")
	}
	if len(pager.pages) != 1 || pager.pages[0] != "222222222222/sweeper_found_resources" {
		t.Errorf("expected a page, got %v", pager.pages)
	}
}

func TestSweep_NukeErrorQuarantinesAndPages(t *testing.T) {
	s, fake, pager := newTestSweeper(t)
	seed(t, s, "333333333333", "AVAILABLE")
	fake.NukeFn = func(string) (cloudaws.NukeResult, error) {
		return cloudaws.NukeResult{}, errors.New("aws-nuke crashed")
	}
	res := s.sweepOnce(context.Background())
	if res.Errored != 1 {
		t.Fatalf("expected 1 errored, got %+v", res)
	}
	if stateOf(t, s, "333333333333") != "QUARANTINED" {
		t.Errorf("nuke-error account must be QUARANTINED")
	}
	if len(pager.pages) != 1 || pager.pages[0] != "333333333333/sweeper_nuke_error" {
		t.Errorf("expected a nuke-error page, got %v", pager.pages)
	}
}

func TestSweep_QuarantinedAccountThatVerifiesCleanStaysQuarantined(t *testing.T) {
	s, fake, _ := newTestSweeper(t)
	// a previously quarantined account
	_, err := s.db.Exec(context.Background(),
		`INSERT INTO env.cloud_account (aws_account_id, state, region, quarantine_reason, quarantine_resources_remaining)
		 VALUES ('444444444444', 'QUARANTINED', 'us-east-1', 'verification_nonempty', 1)`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	fake.NukeFn = func(string) (cloudaws.NukeResult, error) {
		return cloudaws.NukeResult{Verified: true}, nil
	}
	res := s.sweepOnce(context.Background())
	if res.CleanReconfirmed != 1 {
		t.Fatalf("expected the clean re-verify to be counted, got %+v", res)
	}
	// sweeper NEVER auto-clears quarantine
	if stateOf(t, s, "444444444444") != "QUARANTINED" {
		t.Errorf("sweeper must not auto-release a QUARANTINED account")
	}
}
