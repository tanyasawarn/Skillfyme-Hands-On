package cloudaws

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/sts/types"
)

// RealClient is the production cloudaws.Client. It is wired in
// cmd/orchestrator only when CLOUD_ACCOUNTS_ENABLED=true; otherwise the
// service uses FakeClient and none of this runs.
//
// Design: the credential exchange uses the aws-sdk-go-v2 sts client (the
// one SDK already in go.mod). Everything else — the containerised
// aws-nuke run, the post-nuke verification pass, the baseline Terraform
// apply, the Cost Explorer / CUR reads, the account tagging — shells out
// to the `aws` CLI, `aws-nuke`, and `terraform`, exactly as the plan
// describes them (memory.md §5.3: "containerised aws-nuke runner"; §12.3:
// "apply baseline Terraform"). That keeps the binary free of a dozen
// extra SDK modules and matches how the ops image is built (the T3
// workspace image already ships aws/terraform — infra/images/openvscode).
//
// Every shell-out is argv-only (no shell string), rooted at a working
// dir, and time-bounded by the caller's context.
type RealClient struct {
	region string
	// platformAccountID is stamped into the aws-nuke config + used for
	// the PlatformNukeRole trust check.
	platformAccountID string
	// baselineModuleDir is the checked-out infra/account-baseline module
	// (mounted into the orchestrator pod). ApplyBaseline runs tofu/tf here.
	baselineModuleDir string
	// nukeConfigTemplate is the aws-nuke config template path.
	nukeConfigTemplate string
	// tfStateBucket is the platform-managed S3 backend for the per-account
	// baseline state.
	tfStateBucket string
	// curBucket is where the daily CUR export lands.
	curBucket string

	stsClient *sts.Client
}

// RealClientConfig is the subset of orchestrator config the real client
// needs (passed explicitly so this package doesn't import internal/config).
type RealClientConfig struct {
	Region             string
	PlatformAccountID  string
	BaselineModuleDir  string
	NukeConfigTemplate string
	TFStateBucket      string
	CURBucket          string
}

// NewRealClient builds a RealClient using the ambient AWS credential
// chain (IRSA / instance profile / env). Returns an error if the base
// AWS config can't be loaded.
func NewRealClient(ctx context.Context, cfg RealClientConfig) (*RealClient, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.Region))
	if err != nil {
		return nil, fmt.Errorf("cloudaws: load AWS config: %w", err)
	}
	return &RealClient{
		region:             cfg.Region,
		platformAccountID:  cfg.PlatformAccountID,
		baselineModuleDir:  cfg.BaselineModuleDir,
		nukeConfigTemplate: cfg.NukeConfigTemplate,
		tfStateBucket:      cfg.TFStateBucket,
		curBucket:          cfg.CURBucket,
		stsClient:          sts.NewFromConfig(awsCfg),
	}, nil
}

func (c *RealClient) AssumeRoleWithWebIdentity(ctx context.Context, accountID, roleName, webIdentityToken string, ttl time.Duration) (AssumeRoleResult, error) {
	roleArn := fmt.Sprintf("arn:aws:iam::%s:role/%s", accountID, roleName)
	secs := int32(ttl.Seconds())
	if secs < 900 {
		secs = 900
	}
	out, err := c.stsClient.AssumeRoleWithWebIdentity(ctx, &sts.AssumeRoleWithWebIdentityInput{
		RoleArn:          &roleArn,
		RoleSessionName:  strPtr("learner-" + shortHash(webIdentityToken)),
		WebIdentityToken: &webIdentityToken,
		DurationSeconds:  &secs,
	})
	if err != nil {
		return AssumeRoleResult{}, fmt.Errorf("cloudaws: AssumeRoleWithWebIdentity: %w", err)
	}
	return credsFrom(out.Credentials), nil
}

func credsFrom(c *types.Credentials) AssumeRoleResult {
	if c == nil {
		return AssumeRoleResult{}
	}
	return AssumeRoleResult{
		AccessKeyID:     deref(c.AccessKeyId),
		SecretAccessKey: deref(c.SecretAccessKey),
		SessionToken:    deref(c.SessionToken),
		Expiration:      derefTime(c.Expiration),
	}
}

// RunNuke shells out to the containerised aws-nuke against the account,
// then runs the mandatory verification pass. The exact commands are
// wrapped in a script (infra/nuke/run.sh) so the config-generation +
// blind-spot list live with the infra, not here; RealClient just invokes
// it and parses its JSON result.
func (c *RealClient) RunNuke(ctx context.Context, accountID string) (NukeResult, error) {
	script := os.Getenv("CLOUD_NUKE_SCRIPT")
	if script == "" {
		script = "/opt/practice/nuke/run.sh"
	}
	out, err := runJSON(ctx, "", script,
		"--account", accountID,
		"--platform-account", c.platformAccountID,
		"--region", c.region,
		"--config-template", c.nukeConfigTemplate,
		"--json")
	if err != nil {
		return NukeResult{}, fmt.Errorf("cloudaws: nuke run: %w", err)
	}
	var res struct {
		Verified           bool     `json:"verified"`
		ResourcesRemaining int      `json:"resources_remaining"`
		BlindSpotHits      []string `json:"blind_spot_hits"`
		Detail             string   `json:"detail"`
	}
	if uerr := json.Unmarshal(out, &res); uerr != nil {
		return NukeResult{}, fmt.Errorf("cloudaws: parse nuke result: %w (raw: %s)", uerr, truncate(string(out), 300))
	}
	return NukeResult{
		Verified:           res.Verified,
		ResourcesRemaining: res.ResourcesRemaining,
		BlindSpotHits:      res.BlindSpotHits,
		Detail:             res.Detail,
	}, nil
}

func (c *RealClient) PutAccountBudget(ctx context.Context, accountID string, limitUSD float64, thresholds []BudgetThreshold) error {
	pcts := make([]string, 0, len(thresholds))
	for _, t := range thresholds {
		pcts = append(pcts, fmt.Sprintf("%d", t.Percent))
	}
	// aws budgets create-budget + notifications, wrapped so the
	// EventBridge → SNS → orchestrator wiring lives with the infra.
	_, err := runOut(ctx, "", "/opt/practice/budget/put.sh",
		"--account", accountID,
		"--limit", fmt.Sprintf("%.2f", limitUSD),
		"--thresholds", strings.Join(pcts, ","))
	return err
}

func (c *RealClient) DeleteAccountBudget(ctx context.Context, accountID string) error {
	_, err := runOut(ctx, "", "/opt/practice/budget/delete.sh", "--account", accountID)
	return err
}

// ApplyBaseline runs the infra/account-baseline module into the account.
// Assumes OrganizationAccountAccessRole is assumable and the ambient
// creds can do so (the pod's IRSA role is in the Platform account).
func (c *RealClient) ApplyBaseline(ctx context.Context, accountID, attemptID, tenantID string) (string, error) {
	if c.baselineModuleDir == "" {
		return "", fmt.Errorf("cloudaws: BASELINE_MODULE_DIR not set")
	}
	tf := terraformBin()
	stateKey := fmt.Sprintf("account-baseline/%s.tfstate", accountID)

	if _, err := runOut(ctx, c.baselineModuleDir, tf, "init", "-input=false", "-reconfigure",
		"-backend-config=bucket="+c.tfStateBucket,
		"-backend-config=key="+stateKey,
		"-backend-config=region="+c.region,
	); err != nil {
		return "", fmt.Errorf("cloudaws: baseline init: %w", err)
	}
	if _, err := runOut(ctx, c.baselineModuleDir, tf, "apply", "-input=false", "-auto-approve",
		"-var=region="+c.region,
		"-var=attempt_id="+attemptID,
		"-var=tenant_id="+tenantID,
		"-var=platform_account_id="+c.platformAccountID,
	); err != nil {
		return "", fmt.Errorf("cloudaws: baseline apply: %w", err)
	}
	out, err := runOut(ctx, c.baselineModuleDir, tf, "output", "-raw", "learner_sandbox_role_arn")
	if err != nil {
		return "LearnerSandboxRole", nil // the module always names it this
	}
	arn := strings.TrimSpace(string(out))
	if i := strings.LastIndex(arn, "/"); i >= 0 {
		return arn[i+1:], nil
	}
	return "LearnerSandboxRole", nil
}

func (c *RealClient) SetSkuExceptionTag(ctx context.Context, accountID string, skuExceptions []string) error {
	val := "none"
	if len(skuExceptions) > 0 {
		val = strings.Join(skuExceptions, "+")
	}
	_, err := runOut(ctx, "", "aws", "organizations", "tag-resource",
		"--resource-id", accountID,
		"--tags", "Key=practice-engine:sku-exception,Value="+val,
		"--region", c.region)
	return err
}

func (c *RealClient) ClearAccountTags(ctx context.Context, accountID string) error {
	_, err := runOut(ctx, "", "aws", "organizations", "untag-resource",
		"--resource-id", accountID,
		"--tag-keys", "practice-engine:sku-exception",
		"--region", c.region)
	return err
}

func (c *RealClient) GetCostSince(ctx context.Context, accountID string, since time.Time) ([]AccountCost, error) {
	out, err := runJSON(ctx, "", "/opt/practice/cost/ce.sh",
		"--account", accountID,
		"--since", since.Format("2006-01-02"),
		"--json")
	if err != nil {
		return nil, err
	}
	return parseCostJSON(out, "ce")
}

func (c *RealClient) ReconcileFromCUR(ctx context.Context, day time.Time) ([]AccountCost, error) {
	out, err := runJSON(ctx, "", "/opt/practice/cost/cur.sh",
		"--bucket", c.curBucket,
		"--day", day.Format("2006-01-02"),
		"--json")
	if err != nil {
		return nil, err
	}
	return parseCostJSON(out, "cur")
}

// --- helpers ---------------------------------------------------------

func parseCostJSON(b []byte, source string) ([]AccountCost, error) {
	var raw []struct {
		AccountID   string  `json:"account_id"`
		AttemptID   string  `json:"attempt_id"`
		WindowStart string  `json:"window_start"`
		WindowEnd   string  `json:"window_end"`
		AmountUSD   float64 `json:"amount_usd"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("cloudaws: parse cost json: %w", err)
	}
	out := make([]AccountCost, 0, len(raw))
	for _, r := range raw {
		ws, _ := time.Parse(time.RFC3339, r.WindowStart)
		we, _ := time.Parse(time.RFC3339, r.WindowEnd)
		out = append(out, AccountCost{
			AccountID:   r.AccountID,
			AttemptID:   r.AttemptID,
			WindowStart: ws,
			WindowEnd:   we,
			AmountUSD:   r.AmountUSD,
			Source:      source,
		})
	}
	return out, nil
}

func runOut(ctx context.Context, dir, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w (output: %s)", bin, strings.Join(args, " "), err, truncate(string(out), 500))
	}
	return out, nil
}

// runJSON is like runOut but tolerates a leading log line before the JSON.
func runJSON(ctx context.Context, dir, bin string, args ...string) ([]byte, error) {
	out, err := runOut(ctx, dir, bin, args...)
	if err != nil {
		return nil, err
	}
	s := string(out)
	if i := strings.IndexAny(s, "[{"); i > 0 {
		return []byte(s[i:]), nil
	}
	return out, nil
}

func terraformBin() string {
	if b := os.Getenv("TERRAFORM_BIN"); b != "" {
		return b
	}
	if _, err := exec.LookPath("tofu"); err == nil {
		return "tofu"
	}
	return "terraform"
}

func strPtr(s string) *string { return &s }
func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
func derefTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func shortHash(s string) string {
	if len(s) <= 12 {
		return s
	}
	return s[:12]
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
