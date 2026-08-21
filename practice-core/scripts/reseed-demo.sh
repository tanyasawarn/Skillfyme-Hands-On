#!/usr/bin/env bash
# Re-seeds all demo data after running the integration test suite (which
# truncates the shared local Postgres via test-db.ts's truncateAll()).
# Run from practice-core/: ./scripts/reseed-demo.sh
set -euo pipefail
cd "$(dirname "$0")/.."

export DATABASE_URL="${DATABASE_URL:-postgres://practice:practice@localhost:5433/practice_engine}"

docker exec practice-engine-postgres-1 psql -U practice -d practice_engine -c "
INSERT INTO learner.tenant (id, name) VALUES ('11111111-1111-1111-1111-111111111111', 'demo-tenant') ON CONFLICT DO NOTHING;
INSERT INTO learner.user_account (id, tenant_id, email) VALUES ('55555555-5555-5555-5555-555555555555', '11111111-1111-1111-1111-111111111111', 'learner@demo.dev') ON CONFLICT DO NOTHING;
INSERT INTO skill.skill (slug, name, domain) VALUES
  ('k8s.deployments', 'K8s Deployments', 'k8s'),
  ('k8s.yaml', 'K8s YAML', 'k8s'),
  ('kubectl.cli', 'kubectl CLI', 'k8s'),
  ('k8s.services', 'K8s Services', 'k8s'),
  ('k8s.troubleshooting', 'K8s Troubleshooting', 'k8s'),
  ('docker.basics', 'Docker Basics', 'docker'),
  ('linux.cli', 'Linux CLI', 'linux'),
  ('k8s.core', 'K8s Core', 'k8s')
ON CONFLICT (slug) DO NOTHING;
"

npx ts-node scripts/seed-curriculum.ts
npx ts-node scripts/publish-demo-activity.ts

echo "Demo data reseeded."
