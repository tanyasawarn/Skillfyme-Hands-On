-- Doc §3.2: activity spec's curriculum.primary_topic is authored as a
-- stable slug (e.g. "topic.devops.k8s.deployments"), not a topic title or
-- UUID. content.topic had no slug column, so there was no way to resolve
-- a published activity's curriculum.primary_topic back to a topic row
-- (M1.5 curriculum mapping gap, found while wiring activity_topic
-- population into CatalogRepository.publishNewVersion).

ALTER TABLE content.topic ADD COLUMN IF NOT EXISTS slug text;
ALTER TABLE content.module ADD COLUMN IF NOT EXISTS slug text;

-- Backfill is not needed (no rows exist yet at this point in the
-- project), but a real migration on a populated table would need one
-- before adding NOT NULL. Slugs are unique per tenant-scoped course tree;
-- module/topic slugs are unique within their parent, enforced at the
-- application layer (CurriculumRepository) rather than a DB constraint,
-- since composite uniqueness across the variable-depth tree isn't worth
-- a partial index at this table size (doc §2.1: curriculum tree is a
-- product-packaging artifact, expected to be small and admin-curated).

CREATE INDEX IF NOT EXISTS idx_topic_slug ON content.topic (slug);
CREATE INDEX IF NOT EXISTS idx_module_slug ON content.module (slug);
