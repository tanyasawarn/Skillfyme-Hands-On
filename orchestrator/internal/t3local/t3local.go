// Package t3local wires the T3 driver + snapshot manager against REAL
// local infrastructure (k3s from docker-compose, MinIO from
// docker-compose) with a FAKE AWS control plane (cloudaws.FakeClient).
//
// This is the "local-real" T3 mode: everything the orchestrator itself
// runs is real -- a real workspace pod on the real cluster, a real
// credentials file written into that pod, real manifests stored in real
// MinIO, real `terraform`/`kubectl` execution inside the pod via the
// real ExecShell path -- and only the boundary that genuinely needs an
// AWS Organizations account (STS AssumeRoleWithWebIdentity, aws-nuke,
// AWS Budgets, Cost Explorer) is faked.
//
// Every type here satisfies one interface from t3driver or snapshotstate
// and holds only real clients. There is no in-memory fake pod, no
// in-memory blob store -- those live in the *_test.go files of the
// respective packages and are for unit tests, not this mode.
package t3local

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/accountpool"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/snapshotstate"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/t3driver"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/validation"
)

var log = logging.Component("t3local")

// ---------------------------------------------------------------------
// LocalPodManager: real k3s workspace pod (t3driver.PodManager +
// snapshotstate.PodLifecycle).
// ---------------------------------------------------------------------

type LocalPodManager struct {
	prov          *k8s.Provisioner
	editorImage   string
	credsPath     string
	region        string
	fakeAWS       bool
	s3EndpointURL string
}

// NewLocalPodManager builds the pod manager over the orchestrator's
// existing *k8s.Provisioner (same cluster the T1 driver uses).
func NewLocalPodManager(prov *k8s.Provisioner, editorImage, credsPath, region, s3EndpointURL string, fakeAWS bool) *LocalPodManager {
	if credsPath == "" {
		credsPath = k8s.T3DefaultCredsMountPath
	}
	return &LocalPodManager{
		prov:          prov,
		editorImage:   editorImage,
		credsPath:     credsPath,
		region:        region,
		fakeAWS:       fakeAWS,
		s3EndpointURL: s3EndpointURL,
	}
}

func (m *LocalPodManager) StartWorkspacePod(ctx context.Context, in t3driver.StartPodInput) (t3driver.WorkspacePod, error) {
	// A prior namespace for this env id may still be Terminating (a T3
	// Restore re-provisions under the same manifest.EnvID; a retried
	// provision reuses the id too). Creating quota/limitrange into a
	// terminating namespace fails, so wait it out first (bounded).
	if err := m.prov.WaitForNamespaceGone(ctx, in.EnvID, 90*time.Second); err != nil {
		return t3driver.WorkspacePod{}, fmt.Errorf("t3local: waiting for prior namespace to clear: %w", err)
	}
	ns, err := m.prov.CreateT3WorkspacePod(ctx, k8s.StartT3WorkspacePodInput{
		AttemptID:      in.AttemptID,
		EnvID:          in.EnvID,
		EditorImage:    m.editorImage,
		CredsMountPath: m.credsPath,
		Region:         m.region,
		FakeAWS:        m.fakeAWS,
		S3EndpointURL:  m.s3EndpointURL,
	})
	if err != nil {
		return t3driver.WorkspacePod{}, err
	}
	// Block until the workspace container is Ready -- every downstream
	// step (creds write, ExecShell, terraform) execs into it.
	if err := m.prov.WaitForPodReady(ctx, in.EnvID); err != nil {
		return t3driver.WorkspacePod{}, fmt.Errorf("t3local: workspace pod not ready: %w", err)
	}
	log.Info("T3 workspace pod ready", "env_id", in.EnvID, "namespace", ns, "account_id", in.AccountID)
	return t3driver.WorkspacePod{
		Namespace: ns,
		PodName:   k8s.WorkspacePodName,
		EditorURL: "http://127.0.0.1:3000",
	}, nil
}

func (m *LocalPodManager) DeleteWorkspacePod(ctx context.Context, namespace string) error {
	// namespace is "env-<envID>"; Destroy takes the envID.
	envID := strings.TrimPrefix(namespace, "env-")
	return m.prov.DeleteT3Namespace(ctx, envID)
}

// StartWorkspacePodForRestore re-provisions the pod for a resumed
// attempt (snapshotstate.PodLifecycle).
func (m *LocalPodManager) StartWorkspacePodForRestore(ctx context.Context, attemptID, envID, accountID string) (string, error) {
	pod, err := m.StartWorkspacePod(ctx, t3driver.StartPodInput{
		AttemptID:      attemptID,
		EnvID:          envID,
		AccountID:      accountID,
		CredsMountPath: m.credsPath,
		Region:         m.region,
	})
	if err != nil {
		return "", err
	}
	return pod.Namespace, nil
}

// ---------------------------------------------------------------------
// FileCredsWriter: writes the AWS credentials file into the workspace
// pod's shared emptyDir via ExecInPod (credbroker.CredsWriter).
// ---------------------------------------------------------------------

type FileCredsWriter struct {
	prov      *k8s.Provisioner
	credsPath string
	// envIDForAttempt resolves which env's pod to write into. The broker
	// only knows attempt_id; the driver knows the env_id. Wired at
	// Provision time by RegisterAttemptEnv.
	attemptEnv *attemptEnvMap
}

func NewFileCredsWriter(prov *k8s.Provisioner, credsPath string, m *attemptEnvMap) *FileCredsWriter {
	if credsPath == "" {
		credsPath = k8s.T3DefaultCredsMountPath
	}
	return &FileCredsWriter{prov: prov, credsPath: credsPath, attemptEnv: m}
}

func (w *FileCredsWriter) Write(ctx context.Context, attemptID string, creds cloudaws.AssumeRoleResult) error {
	envID, ok := w.attemptEnv.get(attemptID)
	if !ok {
		return fmt.Errorf("t3local: no env registered for attempt %s (creds write)", attemptID)
	}
	credentials := fmt.Sprintf(
		"[default]\naws_access_key_id = %s\naws_secret_access_key = %s\naws_session_token = %s\n",
		creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken,
	)
	// Write to BOTH containers' shared mount -- it's one emptyDir, so a
	// single write via the workspace container is visible in the creds
	// sidecar too, but we exec the workspace container since that's the
	// one every other path uses.
	script := fmt.Sprintf(
		"mkdir -p %s && cat > %s/credentials <<'PE_CREDS_EOF'\n%sPE_CREDS_EOF\nchmod 600 %s/credentials",
		shQuote(w.credsPath), shQuote(w.credsPath), credentials, shQuote(w.credsPath),
	)
	res, err := k8s.ExecInPod(ctx, w.prov, "env-"+envID, k8s.WorkspacePodName, k8s.WorkspaceContainerName, script, 20*time.Second)
	if err != nil {
		return fmt.Errorf("t3local: write creds file: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("t3local: write creds file exit %d: %s", res.ExitCode, res.Stderr)
	}
	log.Debug("brokered creds written into workspace pod", "attempt_id", attemptID, "env_id", envID)
	return nil
}

// ---------------------------------------------------------------------
// StaticTokenSource: a dev "platform IdP" -- mints a short-lived HS256
// JWT keyed on attempt_id (credbroker.TokenSource). In local-real mode
// the fake AWS client accepts any non-empty token, so this only needs to
// be well-formed and non-empty, not verifiable by a real STS.
// ---------------------------------------------------------------------

type StaticTokenSource struct {
	secret   []byte
	clientID string
	ttl      time.Duration
}

func NewStaticTokenSource(secret, clientID string, ttl time.Duration) *StaticTokenSource {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	if secret == "" {
		secret = "local-real-dev-idp-secret"
	}
	return &StaticTokenSource{secret: []byte(secret), clientID: clientID, ttl: ttl}
}

func (s *StaticTokenSource) WebIdentityToken(_ context.Context, attemptID string) (string, error) {
	now := time.Now()
	header := b64json(map[string]string{"alg": "HS256", "typ": "JWT"})
	payload := b64json(map[string]any{
		"sub": attemptID,
		"aud": s.clientID,
		"iss": "https://local-real.idp.practiceengine.dev",
		"iat": now.Unix(),
		"exp": now.Add(s.ttl).Unix(),
	})
	signingInput := header + "." + payload
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(signingInput))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return signingInput + "." + sig, nil
}

// ---------------------------------------------------------------------
// MinioBlobStore: real S3 PutObject/GetObject against the compose MinIO
// (snapshotstate.BlobStore).
// ---------------------------------------------------------------------

type MinioBlobStore struct {
	client *s3.Client
	bucket string
	prefix string // e.g. "s3://practice-snapshots" -> returned in manifest URIs
}

func NewMinioBlobStore(client *s3.Client, bucket, uriPrefix string) *MinioBlobStore {
	return &MinioBlobStore{client: client, bucket: bucket, prefix: strings.TrimSuffix(uriPrefix, "/")}
}

func (b *MinioBlobStore) Put(ctx context.Context, key string, data []byte) (string, error) {
	_, err := b.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})
	if err != nil {
		return "", fmt.Errorf("t3local: minio put %s: %w", key, err)
	}
	uri := b.prefix + "/" + key
	if b.prefix == "" {
		uri = fmt.Sprintf("s3://%s/%s", b.bucket, key)
	}
	return uri, nil
}

func (b *MinioBlobStore) Get(ctx context.Context, key string) ([]byte, error) {
	out, err := b.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(b.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("t3local: minio get %s: %w", key, err)
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

// ---------------------------------------------------------------------
// ExecShellRunner: run a command inside the workspace pod via the real
// orchestrator ExecShell path (snapshotstate.ShellRunner).
// ---------------------------------------------------------------------

type ExecShellRunner struct {
	prov *k8s.Provisioner
}

func NewExecShellRunner(prov *k8s.Provisioner) *ExecShellRunner {
	return &ExecShellRunner{prov: prov}
}

func (r *ExecShellRunner) Run(ctx context.Context, envID string, argv []string) (string, error) {
	// snapshotstate hands us an argv (usually ["sh","-c","..."]); the
	// orchestrator ExecShell takes a single command string and wraps it
	// in bash itself, so collapse argv back to the inner command when
	// it's the ["sh","-c",CMD] shape, else join.
	var cmd string
	if len(argv) == 3 && (argv[0] == "sh" || argv[0] == "/bin/sh") && argv[1] == "-c" {
		cmd = argv[2]
	} else {
		cmd = strings.Join(argv, " ")
	}
	res, err := validation.ExecShell(ctx, r.prov, envID, cmd, 120_000)
	if err != nil {
		return res.Stdout, fmt.Errorf("t3local: exec in %s: %w", envID, err)
	}
	if res.ExitCode != 0 {
		return res.Stdout, fmt.Errorf("t3local: exec in %s exit %d: %s", envID, res.ExitCode, res.Stderr)
	}
	return res.Stdout, nil
}

// ---------------------------------------------------------------------
// PoolReclaimer: adapt accountpool.Manager to
// snapshotstate.AccountReclaimer.
// ---------------------------------------------------------------------

type PoolReclaimer struct {
	pool *accountpool.Manager
}

func NewPoolReclaimer(pool *accountpool.Manager) *PoolReclaimer {
	return &PoolReclaimer{pool: pool}
}

func (p *PoolReclaimer) ReclaimForAttempt(ctx context.Context, attemptID, tenantID, region, preferredAccountID string, budgetUSD float64) (string, string, error) {
	return p.pool.ReclaimForAttempt(ctx, attemptID, tenantID, region, preferredAccountID, budgetUSD)
}

// ---------------------------------------------------------------------
// attemptEnvMap: the driver knows attempt->env; the broker's CredsWriter
// only gets attempt_id. This is the shared lookup, populated by the
// driver wrapper before it starts the broker.
// ---------------------------------------------------------------------

type attemptEnvMap struct {
	mu sync.Mutex
	m  map[string]string
}

func NewAttemptEnvMap() *attemptEnvMap { return &attemptEnvMap{m: map[string]string{}} }

func (a *attemptEnvMap) Set(attemptID, envID string) {
	a.mu.Lock()
	a.m[attemptID] = envID
	a.mu.Unlock()
}

func (a *attemptEnvMap) get(attemptID string) (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	v, ok := a.m[attemptID]
	return v, ok
}

func (a *attemptEnvMap) Delete(attemptID string) {
	a.mu.Lock()
	delete(a.m, attemptID)
	a.mu.Unlock()
}

// compile-time interface checks.
var (
	_ t3driver.PodManager            = (*LocalPodManager)(nil)
	_ snapshotstate.PodLifecycle     = (*LocalPodManager)(nil)
	_ credbrokerCredsWriter          = (*FileCredsWriter)(nil)
	_ credbrokerTokenSource          = (*StaticTokenSource)(nil)
	_ snapshotstate.BlobStore        = (*MinioBlobStore)(nil)
	_ snapshotstate.ShellRunner      = (*ExecShellRunner)(nil)
	_ snapshotstate.AccountReclaimer = (*PoolReclaimer)(nil)
)

// local aliases so the compile-time checks above don't force a
// credbroker import cycle through this file's header (credbroker is
// imported transitively via t3driver anyway).
type credbrokerCredsWriter interface {
	Write(ctx context.Context, attemptID string, creds cloudaws.AssumeRoleResult) error
}
type credbrokerTokenSource interface {
	WebIdentityToken(ctx context.Context, attemptID string) (string, error)
}

// --- small helpers ---------------------------------------------------

func b64json(v any) string {
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
