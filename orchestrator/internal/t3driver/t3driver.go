// Package t3driver implements the driver side of Provision / Connect /
// Destroy for TIER_T3_CLOUD_ACCOUNT (PLAN_PHASE3_PROJECTS.md 3.2 / A6).
//
// Provision = claim a vended sandbox account (accountpool, Stage 2.4) →
// the baseline Terraform is already applied by the claim path → start
// the STS broker for the attempt (credbroker, Stage 2.1) → start the
// workspace pod in the PLATFORM cluster (the editor + CLIs + broker
// sidecar; the risky code runs in the sandbox account, not the pod, so
// no gVisor) → OpenVSCode container (Stage 3.1 image).
//
// Connect = the OpenVSCode WSS URL + terminal WS, terminated at the
// platform and proxied inward — never a routable address into the pod.
//
// Destroy = stop the broker → run the accountpool release path (nuke +
// verify) → tear down the workspace pod.
//
// The K8s pod shape and the OpenVSCode-proxy plumbing live behind
// PodManager (a small interface satisfied by internal/k8s in production
// and by a fake here), so the whole driver has real unit tests with no
// cluster and no AWS.
package t3driver

import (
	"context"
	"fmt"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/accountpool"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/credbroker"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
)

var log = logging.Component("t3driver")

// WorkspacePod is one T3 workspace pod in the platform cluster.
type WorkspacePod struct {
	Namespace   string
	PodName     string
	EditorURL   string // internal (127.0.0.1) OpenVSCode URL the proxy targets
	TerminalCmd []string
}

// PodManager is what the driver needs from the K8s layer. In production
// this is satisfied by internal/k8s (a TierT3CloudAccount pod shape:
// editor container + broker sidecar, no gVisor); here a fake stands in.
type PodManager interface {
	StartWorkspacePod(ctx context.Context, in StartPodInput) (WorkspacePod, error)
	DeleteWorkspacePod(ctx context.Context, namespace string) error
}

type StartPodInput struct {
	AttemptID   string
	EnvID       string
	AccountID   string
	RoleName    string
	EditorImage string
	Region      string
	// CredsMountPath is the emptyDir the broker sidecar writes creds to
	// and the editor container reads them from.
	CredsMountPath string
}

// SessionTokenMinter mints the short-lived, scoped token the WS gateway
// checks before proxying the editor/terminal WS (reuses the Phase-1
// mechanism — same as ConnectResponse.session_token).
type SessionTokenMinter interface {
	Register(attemptID, envID string) (string, error)
}

// Config for the driver.
type Config struct {
	EditorImage         string
	Region              string
	WSGatewayBaseURL    string
	CredsMountPath      string
	CredBrokerTTL       time.Duration
	CredRefreshFraction float64
}

// Driver is the T3 tier driver.
type Driver struct {
	cfg    Config
	pool   *accountpool.Manager
	broker *credbroker.Registry
	pods   PodManager
	tokens SessionTokenMinter
	aws    cloudaws.Client
	// tokenSource + credsWriter are what the broker needs; injected so a
	// deployment supplies the real platform-IdP client + emptyDir writer.
	tokenSource credbroker.TokenSource
	credsWriter credbroker.CredsWriter
}

func NewDriver(
	cfg Config,
	pool *accountpool.Manager,
	broker *credbroker.Registry,
	pods PodManager,
	tokens SessionTokenMinter,
	aws cloudaws.Client,
	ts credbroker.TokenSource,
	cw credbroker.CredsWriter,
) *Driver {
	if cfg.CredsMountPath == "" {
		cfg.CredsMountPath = "/var/run/secrets/aws"
	}
	return &Driver{
		cfg: cfg, pool: pool, broker: broker, pods: pods,
		tokens: tokens, aws: aws, tokenSource: ts, credsWriter: cw,
	}
}

// ProvisionInput is what the gRPC Provision handler passes for a T3 tier.
type ProvisionInput struct {
	AttemptID     string
	TenantID      string
	EnvID         string
	Region        string
	BudgetUSD     float64
	SkuExceptions []string
}

// ProvisionResult is the driver's output.
type ProvisionResult struct {
	EnvID         string
	AccountID     string
	Namespace     string
	CredsExpireAt time.Time
}

// Provision claims an account, starts the broker + workspace pod, and
// returns once the pod is up with brokered creds.
func (d *Driver) Provision(ctx context.Context, in ProvisionInput) (ProvisionResult, error) {
	region := in.Region
	if region == "" {
		region = d.cfg.Region
	}

	claim, err := d.pool.Claim(ctx, accountpool.ClaimInput{
		AttemptID:     in.AttemptID,
		TenantID:      in.TenantID,
		Region:        region,
		BudgetUSD:     in.BudgetUSD,
		SkuExceptions: in.SkuExceptions,
	})
	if err != nil {
		return ProvisionResult{}, fmt.Errorf("t3driver: claim account: %w", err)
	}

	// Start the STS broker (blocking initial mint), so the pod comes up
	// with valid creds already on disk.
	expireAt, err := d.broker.Add(ctx, credbroker.Config{
		AttemptID:       in.AttemptID,
		AccountID:       claim.AccountID,
		RoleName:        claim.RoleName,
		CredTTL:         d.cfg.CredBrokerTTL,
		RefreshFraction: d.cfg.CredRefreshFraction,
	}, d.aws, d.credsWriter, d.tokenSource)
	if err != nil {
		// roll the account back so it isn't stuck IN_USE
		_ = d.pool.Release(context.Background(), claim.AccountID)
		return ProvisionResult{}, fmt.Errorf("t3driver: start broker: %w", err)
	}

	pod, err := d.pods.StartWorkspacePod(ctx, StartPodInput{
		AttemptID:      in.AttemptID,
		EnvID:          in.EnvID,
		AccountID:      claim.AccountID,
		RoleName:       claim.RoleName,
		EditorImage:    d.cfg.EditorImage,
		Region:         region,
		CredsMountPath: d.cfg.CredsMountPath,
	})
	if err != nil {
		d.broker.StopFor(in.AttemptID)
		_ = d.pool.Release(context.Background(), claim.AccountID)
		return ProvisionResult{}, fmt.Errorf("t3driver: start workspace pod: %w", err)
	}

	log.Info("T3 environment provisioned",
		"env_id", in.EnvID, "attempt_id", in.AttemptID,
		"account_id", claim.AccountID, "namespace", pod.Namespace)
	return ProvisionResult{
		EnvID:         in.EnvID,
		AccountID:     claim.AccountID,
		Namespace:     pod.Namespace,
		CredsExpireAt: expireAt,
	}, nil
}

// ConnectResult is the T3 Connect output.
type ConnectResult struct {
	EditorURL     string // WSS URL, terminated at the platform
	TerminalWSURL string
	SessionToken  string
	ExpiresAt     string
}

// Connect returns the editor + terminal WS URLs for a READY T3 env.
// Both go through the platform WS gateway (terminated there, proxied
// inward over the control-plane channel) — no routable address into the
// pod.
func (d *Driver) Connect(_ context.Context, attemptID, envID string) (ConnectResult, error) {
	token, err := d.tokens.Register(attemptID, envID)
	if err != nil {
		return ConnectResult{}, fmt.Errorf("t3driver: mint session token: %w", err)
	}
	base := d.cfg.WSGatewayBaseURL
	return ConnectResult{
		EditorURL:     fmt.Sprintf("%s/v1/env/%s/editor?session=%s", base, envID, token),
		TerminalWSURL: fmt.Sprintf("%s/v1/env/%s/terminal?session=%s", base, envID, token),
		SessionToken:  token,
	}, nil
}

// Destroy runs the T3 release path: stop the broker → accountpool
// release (nuke + verify) → delete the workspace pod. Idempotent.
func (d *Driver) Destroy(ctx context.Context, attemptID, envID, accountID, namespace string) error {
	// 1. stop refreshing creds — outstanding creds expire within the hour
	d.broker.StopFor(attemptID)

	// 2. release the account (NUKING → nuke + verify → AVAILABLE | QUARANTINED)
	if accountID != "" {
		if err := d.pool.Release(ctx, accountID); err != nil {
			log.Error("t3driver: account release failed", "account_id", accountID, "err", err)
			// still tear the pod down
		}
	}

	// 3. delete the workspace pod
	if namespace != "" {
		if err := d.pods.DeleteWorkspacePod(ctx, namespace); err != nil {
			return fmt.Errorf("t3driver: delete workspace pod: %w", err)
		}
	}
	log.Info("T3 environment destroyed", "env_id", envID, "attempt_id", attemptID, "account_id", accountID)
	return nil
}
