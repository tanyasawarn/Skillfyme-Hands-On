/**
 * Loads the DevOps skill graph derived from Skillfyme's "DevOps with AI"
 * curriculum (all 7 modules: Core Concepts, CI/CD Tools, Containerisation,
 * Cloud, IaC/Terraform, Monitoring/Observability, DevOps+AI). Curriculum
 * document is the source of truth for skill names, domain grouping, and
 * teaching-order dependencies -- this script does not invent skills
 * outside that scope.
 *
 * Domains map 1:1 to curriculum modules (module number kept in comments
 * for traceability back to the PDF). Edges are REQUIRES only: "skill X
 * cannot be attempted before skill Y is taught", matching the actual
 * session order in the curriculum, not assumed prerequisites.
 *
 * Idempotent: createSkill relies on the unique slug constraint (rerun
 * fails loudly on duplicate rather than silently double-inserting --
 * intentional, since a silent partial reseed would be worse than a
 * visible error); addEdge already no-ops on conflict.
 *
 * Run with: npx ts-node -r tsconfig-paths/register scripts/seed-skills.ts
 */
import { Kysely, PostgresDialect } from 'kysely';
import { Pool } from 'pg';
import type { Database } from '../src/db/schema';
import { SkillRepository } from '../src/modules/skill/skill.repository';

interface SkillDef {
  slug: string;
  name: string;
  domain: string;
  description: string;
}

interface EdgeDef {
  from: string; // prerequisite (must be learned first)
  to: string; // dependent skill
}

// --- Module 1: DevOps Core Concepts (curriculum pg.6-10) -----------------
const CORE: SkillDef[] = [
  { slug: 'devops.fundamentals', name: 'DevOps Fundamentals', domain: 'core', description: 'DevOps lifecycle, tools landscape, and DevOps-on-cloud basics.' },
  { slug: 'linux.cli', name: 'Linux Fundamentals for DevOps', domain: 'core', description: 'Shell navigation, file operations, and process basics on Linux.' },
  { slug: 'devops.gitops-evolution', name: 'CI/CD to GitOps Evolution', domain: 'core', description: 'History and principles of the shift from CI/CD to GitOps.' },
  { slug: 'iac.fundamentals', name: 'Infrastructure as Code Fundamentals', domain: 'core', description: 'IaC principles: automation, consistency, versioning, scalability.' },
  { slug: 'microservices.fundamentals', name: 'Microservices Architecture Principles', domain: 'core', description: 'Modularity, bounded contexts, decentralized data management.' },
  { slug: 'microservices.devops-impact', name: 'Microservices Impact on DevOps', domain: 'core', description: 'CI/CD for independently deployed services; service discovery, load balancing.' },
  { slug: 'devsecops.fundamentals', name: 'DevSecOps Principles', domain: 'core', description: 'Shift-left security, security automation, vulnerability scanning.' },
  { slug: 'devsecops.container-security-tools', name: 'Container Security Tooling', domain: 'core', description: 'Docker Bench, Clair, and infrastructure-security-as-code practices.' },
  { slug: 'iac.terraform-vs-cloudformation', name: 'Terraform vs CloudFormation', domain: 'core', description: 'Comparing declarative IaC tools and when to use each.' },
  { slug: 'git.basics', name: 'Git Basic Workflow', domain: 'core', description: 'Commits, branches, tags, revert/reset, .gitignore.' },
  { slug: 'git.branching-strategies', name: 'Git Branching Strategies', domain: 'core', description: 'Feature branching, release branching, hotfixes, conflict resolution.' },
  { slug: 'git.internals', name: 'Git Internals and Advanced Branching', domain: 'core', description: 'Git objects (blobs/trees/commits/tags), rebase vs merge, git hooks.' },
  { slug: 'git.workflow-patterns', name: 'GitFlow / GitHub Flow / GitLab Flow', domain: 'core', description: 'Team collaboration workflow patterns and release tagging strategy.' },
  { slug: 'git.release-management', name: 'Git Release Management', domain: 'core', description: 'Semantic versioning, trunk-based development vs feature branches.' },
];

// --- Module 2: CI/CD Tools (curriculum pg.11-14) --------------------------
const CICD: SkillDef[] = [
  { slug: 'jenkins.basics', name: 'Jenkins Job Creation & Plugins', domain: 'cicd', description: 'Freestyle vs pipeline jobs, plugin management, auth/authz basics.' },
  { slug: 'jenkins.pipeline-as-code', name: 'Jenkins Pipeline as Code', domain: 'cicd', description: 'Declarative Jenkinsfile syntax: stages, steps, version-controlled pipelines.' },
  { slug: 'jenkins.security-integration', name: 'Security Tools in Jenkins Pipelines', domain: 'cicd', description: 'SonarQube/Checkmarx integration, security gates, vulnerability assessment.' },
  { slug: 'jenkins.distributed-builds', name: 'Jenkins Master-Slave Architecture', domain: 'cicd', description: 'Distributed builds, agent scaling, load balancing across Jenkins nodes.' },
  { slug: 'jenkins.advanced-pipelines', name: 'Advanced Jenkins Pipeline Config', domain: 'cicd', description: 'Env variables, caching, retries, HA/disaster-recovery configuration.' },
  { slug: 'gitlab.cicd-pipelines', name: 'GitLab CI/CD Pipelines', domain: 'cicd', description: 'Pipeline YAML, multi-project pipelines, cross-project dependencies.' },
  { slug: 'github.actions-workflows', name: 'GitHub Actions Advanced Workflows', domain: 'cicd', description: 'Matrix builds, conditional steps, environment deployments, PR integration.' },
  { slug: 'cicd.troubleshooting', name: 'CI/CD Pipeline Troubleshooting', domain: 'cicd', description: 'Diagnosing and optimizing failing or slow pipeline runs across tools.' },
];

// --- Module 3: Containerisation with Docker & Kubernetes (pg.15-20) ------
const CONTAINERS: SkillDef[] = [
  { slug: 'docker.basics', name: 'Docker Images & DockerHub', domain: 'containers', description: 'Docker architecture, Dockerfile syntax, image build/publish.' },
  { slug: 'docker.networking', name: 'Docker Networking', domain: 'containers', description: 'Bridge networks, host networking, CNIs, overlay networks.' },
  { slug: 'docker.swarm', name: 'Docker Swarm Mode', domain: 'containers', description: 'Manager/worker nodes, service orchestration, stack deployment, HA.' },
  { slug: 'k8s.architecture', name: 'Kubernetes Architecture', domain: 'containers', description: 'Control plane components: kube-apiserver, scheduler, etcd, kubelet.' },
  { slug: 'k8s.pods', name: 'Kubernetes Pods', domain: 'containers', description: 'Pod anatomy, multi-container pods, shared storage and networking.' },
  { slug: 'k8s.deployments', name: 'Kubernetes Deployments', domain: 'containers', description: 'Declarative deployment management, rolling updates, rollback strategies.' },
  { slug: 'k8s.services', name: 'Kubernetes Services', domain: 'containers', description: 'ClusterIP, NodePort, LoadBalancer, ExternalName; service discovery.' },
  { slug: 'k8s.config-secrets', name: 'Kubernetes ConfigMaps & Secrets', domain: 'containers', description: 'Application configuration and sensitive data management.' },
  { slug: 'k8s.helm', name: 'Helm Package Management', domain: 'containers', description: 'Templating and packaging Kubernetes applications with Helm.' },
  { slug: 'k8s.statefulsets', name: 'Kubernetes StatefulSets', domain: 'containers', description: 'Stateful application deployment, pod identity, ordered scaling.' },
  { slug: 'k8s.storage', name: 'Kubernetes Persistent Volumes', domain: 'containers', description: 'Storage classes, volume plugins, dynamic provisioning, PV/PVC.' },
  { slug: 'k8s.scheduling', name: 'Advanced Kubernetes Scheduling', domain: 'containers', description: 'Node/pod affinity, anti-affinity, taints and tolerations.' },
  { slug: 'k8s.resource-management', name: 'Kubernetes Resource Management', domain: 'containers', description: 'ResourceQuotas and LimitRanges for namespace-level constraints.' },
  { slug: 'k8s.production-deployments', name: 'Kubernetes Production Deployment Strategies', domain: 'containers', description: 'Canary and blue/green deployments in Kubernetes.' },
  { slug: 'k8s.autoscaling', name: 'Kubernetes Autoscaling', domain: 'containers', description: 'HPA, Cluster Autoscaler, Vertical Pod Autoscaler.' },
  { slug: 'k8s.troubleshooting', name: 'Kubernetes Troubleshooting', domain: 'containers', description: 'Diagnosing failed deployments, resource contention, scheduling issues.' },
  { slug: 'istio.traffic-management', name: 'Istio Service Mesh Traffic Management', domain: 'containers', description: 'VirtualServices, request routing, traffic shifting, load balancing.' },
  { slug: 'istio.mtls-security', name: 'Istio mTLS & Distributed Tracing', domain: 'containers', description: 'Service-to-service mTLS auth, Jaeger distributed tracing.' },
  { slug: 'tekton.pipelines', name: 'Tekton CI/CD Pipelines', domain: 'containers', description: 'Tasks, Pipelines, and PipelineRuns for in-cluster CI/CD.' },
  { slug: 'gitops.fluxcd-argocd', name: 'GitOps with FluxCD & Argo CD', domain: 'containers', description: 'Declarative infrastructure and continuous delivery via GitOps tooling.' },
];

// --- Module 4: DevOps Cloud Certification (pg.21-23) ----------------------
const CLOUD: SkillDef[] = [
  { slug: 'cloud.twelve-factor', name: 'Twelve-Factor App Methodology', domain: 'cloud', description: 'Cloud-native design patterns: environment parity, config, backing services.' },
  { slug: 'cloud.k8s-serverless', name: 'Kubernetes & Serverless Architectures', domain: 'cloud', description: 'Choosing/combining container orchestration and serverless compute.' },
  { slug: 'cloud.progressive-delivery', name: 'Progressive Delivery (Canary/Blue-Green)', domain: 'cloud', description: 'Gradual rollout strategies and feature-flag-driven delivery.' },
  { slug: 'cloud.zero-trust-security', name: 'Zero Trust Security Model', domain: 'cloud', description: 'Identity-based access control and least-privilege for cloud-native systems.' },
  { slug: 'cloud.mtls-spiffe', name: 'mTLS & SPIFFE/SPIRE Identity', domain: 'cloud', description: 'Secure service-to-service communication and workload identity.' },
  { slug: 'cloud.aws-core-services', name: 'AWS Core Services', domain: 'cloud', description: 'Compute, storage, networking, and database services on AWS.' },
  { slug: 'cloud.azure-core-services', name: 'Azure Core Services', domain: 'cloud', description: 'VMs, AKS, Azure Functions, Azure DevOps and ARM.' },
  { slug: 'cloud.aws-lambda-serverless', name: 'AWS Lambda & Serverless Computing', domain: 'cloud', description: 'Building and deploying serverless functions on AWS.' },
  { slug: 'cloud.aws-vs-azure', name: 'AWS vs Azure Platform Selection', domain: 'cloud', description: 'Contrasting scalability, pricing, and features to choose a cloud platform.' },
  { slug: 'cloud.api-container-security', name: 'Securing APIs, Containers & Orchestrators', domain: 'cloud', description: 'Monitoring and auditing cloud-native workloads for compliance and threats.' },
];

// --- Module 5: Infrastructure as Code / Terraform (pg.24-26) --------------
const IAC: SkillDef[] = [
  { slug: 'ansible.basics', name: 'Ansible Basics & Advanced', domain: 'iac', description: 'Playbooks and configuration management fundamentals.' },
  { slug: 'terraform.basics', name: 'Terraform Configuration Language', domain: 'iac', description: 'Providers, resources, variables, output/dynamic blocks, state management.' },
  { slug: 'terraform.state-management', name: 'Terraform State Management', domain: 'iac', description: 'State file formats, state locking, and handling concurrent operations.' },
  { slug: 'terraform.modules-workspaces', name: 'Terraform Modules & Workspaces', domain: 'iac', description: 'Reusable modules, multi-environment workspaces.' },
  { slug: 'terraform.cloud-enterprise', name: 'Terraform Cloud & Enterprise', domain: 'iac', description: 'Remote state, collaboration, RBAC, Sentinel policy-as-code.' },
  { slug: 'terraform.remote-state-backends', name: 'Terraform Remote State Backends', domain: 'iac', description: 'Remote state storage strategies: Terraform Cloud, S3, Azure Blob.' },
];

// --- Module 6: Monitoring, Logging, Observability (pg.27-28) --------------
const OBSERVABILITY: SkillDef[] = [
  { slug: 'observability.prometheus-basics', name: 'Prometheus & Grafana Basics', domain: 'observability', description: 'Metrics collection, querying, dashboarding fundamentals.' },
  { slug: 'observability.elk-basics', name: 'ELK Stack Basics', domain: 'observability', description: 'Elasticsearch indexing, Logstash pipelines, Kibana visualization.' },
  { slug: 'observability.prometheus-advanced', name: 'Advanced Prometheus & Grafana', domain: 'observability', description: 'Service discovery, recording rules/alerts, dashboard templating.' },
  { slug: 'observability.tracing', name: 'Distributed Tracing with Jaeger', domain: 'observability', description: 'Request tracing across microservices for latency/failure diagnosis.' },
  { slug: 'observability.alerting', name: 'Alerting & Dashboard Design', domain: 'observability', description: 'Prometheus Alertmanager and Grafana alerting/dashboard practices.' },
  { slug: 'observability.scaling-infra', name: 'Scaling Monitoring Infrastructure', domain: 'observability', description: 'Strategies for scaling Prometheus/Grafana under growing load.' },
  { slug: 'observability.nagios-alerting', name: 'Nagios XI Infrastructure Alerting', domain: 'observability', description: 'Infrastructure health checks and alerting with Nagios XI.' },
];

// --- Module 7: DevOps with AI (pg.28-30) ----------------------------------
const AI_OPS: SkillDef[] = [
  { slug: 'aiops.fundamentals', name: 'AI in DevOps Fundamentals', domain: 'aiops', description: 'AI/ML relevance to DevOps: automation, predictive analytics, decisioning.' },
  { slug: 'aiops.cicd-automation', name: 'AI-Driven CI/CD Automation', domain: 'aiops', description: 'AI-assisted testing, deployment automation, code quality analysis.' },
  { slug: 'aiops.predictive-analytics', name: 'Predictive Analytics for DevOps', domain: 'aiops', description: 'Performance forecasting, capacity planning, anomaly detection.' },
  { slug: 'aiops.incident-management', name: 'AI-Driven Incident Management', domain: 'aiops', description: 'Automated root-cause analysis and MTTR reduction.' },
  { slug: 'aiops.security', name: 'AIOps Security', domain: 'aiops', description: 'AI-driven threat detection, vulnerability management, compliance.' },
  { slug: 'aiops.monitoring', name: 'AI-Enhanced Monitoring', domain: 'aiops', description: 'Anomaly detection and predictive alerts layered on existing monitoring.' },
  { slug: 'aiops.self-healing', name: 'AI-Driven Self-Healing Systems', domain: 'aiops', description: 'Automated remediation, dynamic scaling, fault tolerance via AI.' },
];

const SKILLS: SkillDef[] = [...CORE, ...CICD, ...CONTAINERS, ...CLOUD, ...IAC, ...OBSERVABILITY, ...AI_OPS];

// Curriculum module order (1-7), used for domain_order -- NOT alphabetical.
// This is why the catalog previously showed "DevOps with AI" (module 7)
// above "DevOps Core Concepts" (module 1): domain sorted as plain text
// puts 'aiops' before 'core'. domain_order fixes that by ranking domains
// in the order the curriculum actually teaches them.
const DOMAIN_ORDER: Record<string, number> = {
  core: 1,
  cicd: 2,
  containers: 3,
  cloud: 4,
  iac: 5,
  observability: 6,
  aiops: 7,
};

// REQUIRES edges: `from` must be learned before `to`, matching curriculum
// session order (not assumed prerequisites -- traced to actual sequence).
const EDGES: EdgeDef[] = [
  // Core Concepts internal sequence
  { from: 'devops.fundamentals', to: 'linux.cli' },
  { from: 'linux.cli', to: 'devops.gitops-evolution' },
  { from: 'devops.gitops-evolution', to: 'iac.fundamentals' },
  { from: 'iac.fundamentals', to: 'microservices.fundamentals' },
  { from: 'microservices.fundamentals', to: 'microservices.devops-impact' },
  { from: 'microservices.devops-impact', to: 'devsecops.fundamentals' },
  { from: 'devsecops.fundamentals', to: 'devsecops.container-security-tools' },
  { from: 'iac.fundamentals', to: 'iac.terraform-vs-cloudformation' },
  { from: 'devsecops.container-security-tools', to: 'git.basics' },
  { from: 'git.basics', to: 'git.branching-strategies' },
  { from: 'git.branching-strategies', to: 'git.internals' },
  { from: 'git.internals', to: 'git.workflow-patterns' },
  { from: 'git.workflow-patterns', to: 'git.release-management' },

  // CI/CD Tools builds on Git + core
  { from: 'git.release-management', to: 'jenkins.basics' },
  { from: 'jenkins.basics', to: 'jenkins.pipeline-as-code' },
  { from: 'jenkins.pipeline-as-code', to: 'jenkins.security-integration' },
  { from: 'jenkins.security-integration', to: 'jenkins.distributed-builds' },
  { from: 'jenkins.distributed-builds', to: 'jenkins.advanced-pipelines' },
  { from: 'jenkins.pipeline-as-code', to: 'gitlab.cicd-pipelines' },
  { from: 'gitlab.cicd-pipelines', to: 'github.actions-workflows' },
  { from: 'github.actions-workflows', to: 'cicd.troubleshooting' },

  // Containerisation builds on CI/CD
  { from: 'cicd.troubleshooting', to: 'docker.basics' },
  { from: 'docker.basics', to: 'docker.networking' },
  { from: 'docker.networking', to: 'docker.swarm' },
  { from: 'docker.swarm', to: 'k8s.architecture' },
  { from: 'k8s.architecture', to: 'k8s.pods' },
  { from: 'k8s.pods', to: 'k8s.deployments' },
  { from: 'k8s.deployments', to: 'k8s.services' },
  { from: 'k8s.services', to: 'k8s.config-secrets' },
  { from: 'k8s.config-secrets', to: 'k8s.helm' },
  { from: 'k8s.helm', to: 'k8s.statefulsets' },
  { from: 'k8s.statefulsets', to: 'k8s.storage' },
  { from: 'k8s.storage', to: 'k8s.scheduling' },
  { from: 'k8s.scheduling', to: 'k8s.resource-management' },
  { from: 'k8s.resource-management', to: 'k8s.production-deployments' },
  { from: 'k8s.production-deployments', to: 'k8s.autoscaling' },
  { from: 'k8s.autoscaling', to: 'k8s.troubleshooting' },
  { from: 'k8s.deployments', to: 'istio.traffic-management' },
  { from: 'istio.traffic-management', to: 'istio.mtls-security' },
  { from: 'k8s.deployments', to: 'tekton.pipelines' },
  { from: 'tekton.pipelines', to: 'gitops.fluxcd-argocd' },

  // Cloud builds on containers
  { from: 'k8s.troubleshooting', to: 'cloud.twelve-factor' },
  { from: 'cloud.twelve-factor', to: 'cloud.k8s-serverless' },
  { from: 'cloud.k8s-serverless', to: 'cloud.progressive-delivery' },
  { from: 'cloud.progressive-delivery', to: 'cloud.zero-trust-security' },
  { from: 'cloud.zero-trust-security', to: 'cloud.mtls-spiffe' },
  { from: 'cloud.mtls-spiffe', to: 'cloud.api-container-security' },
  { from: 'cloud.api-container-security', to: 'cloud.aws-core-services' },
  { from: 'cloud.aws-core-services', to: 'cloud.aws-lambda-serverless' },
  { from: 'cloud.aws-lambda-serverless', to: 'cloud.azure-core-services' },
  { from: 'cloud.azure-core-services', to: 'cloud.aws-vs-azure' },

  // IaC/Terraform builds on core IaC fundamentals + cloud
  { from: 'iac.fundamentals', to: 'ansible.basics' },
  { from: 'ansible.basics', to: 'terraform.basics' },
  { from: 'cloud.aws-vs-azure', to: 'terraform.basics' },
  { from: 'terraform.basics', to: 'terraform.state-management' },
  { from: 'terraform.state-management', to: 'terraform.modules-workspaces' },
  { from: 'terraform.modules-workspaces', to: 'terraform.cloud-enterprise' },
  { from: 'terraform.cloud-enterprise', to: 'terraform.remote-state-backends' },

  // Observability builds on containers
  { from: 'k8s.deployments', to: 'observability.prometheus-basics' },
  { from: 'observability.prometheus-basics', to: 'observability.nagios-alerting' },
  { from: 'observability.nagios-alerting', to: 'observability.elk-basics' },
  { from: 'observability.elk-basics', to: 'observability.prometheus-advanced' },
  { from: 'observability.prometheus-advanced', to: 'observability.tracing' },
  { from: 'observability.tracing', to: 'observability.alerting' },
  { from: 'observability.alerting', to: 'observability.scaling-infra' },

  // DevOps+AI builds on CI/CD + observability (final module)
  { from: 'jenkins.pipeline-as-code', to: 'aiops.fundamentals' },
  { from: 'aiops.fundamentals', to: 'aiops.cicd-automation' },
  { from: 'observability.prometheus-advanced', to: 'aiops.predictive-analytics' },
  { from: 'aiops.predictive-analytics', to: 'aiops.incident-management' },
  { from: 'aiops.incident-management', to: 'aiops.security' },
  { from: 'observability.tracing', to: 'aiops.monitoring' },
  { from: 'aiops.monitoring', to: 'aiops.self-healing' },
];

async function main() {
  const db = new Kysely<Database>({
    dialect: new PostgresDialect({
      pool: new Pool({
        connectionString: process.env.DATABASE_URL ?? 'postgres://practice:practice@localhost:5433/practice_engine',
      }),
    }),
  });

  const skills = new SkillRepository(db);

  console.log(`Inserting ${SKILLS.length} skills across ${new Set(SKILLS.map((s) => s.domain)).size} domains...`);
  const idBySlug = new Map<string, string>();
  const domainSeq = new Map<string, number>();
  for (const s of SKILLS) {
    const sequence = (domainSeq.get(s.domain) ?? 0) + 1;
    domainSeq.set(s.domain, sequence);
    const row = await skills.createSkill({
      slug: s.slug,
      name: s.name,
      domain: s.domain,
      description: s.description,
      domainOrder: DOMAIN_ORDER[s.domain] ?? 999,
      sequence,
    });
    idBySlug.set(s.slug, row.id);
    console.log(`  + ${s.slug}`);
  }

  console.log(`Inserting ${EDGES.length} REQUIRES edges...`);
  for (const e of EDGES) {
    const fromId = idBySlug.get(e.from);
    const toId = idBySlug.get(e.to);
    if (!fromId || !toId) {
      console.warn(`  ! skipping edge ${e.from} -> ${e.to}: unknown slug`);
      continue;
    }
    await skills.addEdge({ fromSkillId: fromId, toSkillId: toId, type: 'REQUIRES' });
  }

  console.log('Rebuilding skill.skill_closure...');
  await skills.rebuildClosure();

  console.log(`Done: ${SKILLS.length} skills, ${EDGES.length} edges seeded.`);
  await db.destroy();
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
