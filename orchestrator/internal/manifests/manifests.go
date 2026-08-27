// Package manifests embeds large static K8s manifests this codebase's
// Go code applies directly (via clusterbootstrap.ApplyManifestURL,
// which accepts a local file path despite its name) -- kept as a
// separate package (not top-level manifests/, which is the
// human-facing, kubectl-applied-by-hand location for T1/T2 cluster
// bootstrap manifests) because go:embed can only reach files within its
// own package's directory tree, not a sibling directory via "..".
package manifests

import _ "embed"

// IstioMinimalYAML is `istioctl manifest generate --set profile=minimal`
// (istioctl 1.30.3) -- see the file's own header comment for full
// provenance and regeneration instructions.
//
//go:embed istio-minimal.gen.yaml
var IstioMinimalYAML string

// ArgoCDCoreInstallYAML is Argo CD's own upstream "core install" manifest
// (application-controller, repo-server, redis, applicationset-controller,
// and the Application/ApplicationSet/AppProject CRDs -- no
// argocd-server/dex/UI). See the file's own header comment for full
// provenance and regeneration instructions.
//
//go:embed argocd-core-install.gen.yaml
var ArgoCDCoreInstallYAML string
