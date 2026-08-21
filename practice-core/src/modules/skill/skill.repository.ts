import { Inject, Injectable } from '@nestjs/common';
import type { Kysely } from 'kysely';
import { sql } from 'kysely';
import { KYSELY } from '../../db/database.module';
import type { Database, SkillEdgeType } from '../../db/schema';

export interface SkillEdgeInput {
  fromSkillId: string;
  toSkillId: string;
  type: SkillEdgeType;
  strength?: number;
}

@Injectable()
export class SkillRepository {
  constructor(@Inject(KYSELY) private readonly db: Kysely<Database>) {}

  async createSkill(input: {
    slug: string;
    name: string;
    domain: string;
    description?: string;
    decayHalfLifeDays?: number;
    domainOrder?: number;
    sequence?: number;
    courseSlug?: string;
  }) {
    return this.db
      .insertInto('skill.skill')
      .values({
        slug: input.slug,
        name: input.name,
        domain: input.domain,
        description: input.description ?? null,
        ...(input.decayHalfLifeDays !== undefined
          ? { decay_half_life_days: input.decayHalfLifeDays }
          : {}),
        ...(input.domainOrder !== undefined
          ? { domain_order: input.domainOrder }
          : {}),
        ...(input.sequence !== undefined ? { sequence: input.sequence } : {}),
        ...(input.courseSlug !== undefined
          ? { course_slug: input.courseSlug }
          : {}),
      })
      .returningAll()
      .executeTakeFirstOrThrow();
  }

  async addEdge(edge: SkillEdgeInput) {
    await this.db
      .insertInto('skill.skill_edge')
      .values({
        from_skill_id: edge.fromSkillId,
        to_skill_id: edge.toSkillId,
        type: edge.type,
        ...(edge.strength !== undefined ? { strength: edge.strength } : {}),
      })
      .onConflict((oc) =>
        oc.columns(['from_skill_id', 'to_skill_id', 'type']).doNothing(),
      )
      .execute();
  }

  /**
   * Rebuilds skill.skill_closure from skill.skill_edge via a recursive CTE.
   * Doc §2.2: "materialise the closure ... refreshed on skill-graph publish
   * ... rebuild is sub-second; do it synchronously inside the publish
   * transaction." Only REQUIRES and BUILDS_ON edges form the prerequisite
   * closure that eligibility gating (§2.3) walks; SIBLING/SPECIALIZES/
   * SUPERSEDES are graph relationships but not prerequisite paths.
   */
  async rebuildClosure(): Promise<void> {
    await sql`
      TRUNCATE skill.skill_closure;

      WITH RECURSIVE walk (ancestor_id, descendant_id, depth, edge_types, path) AS (
        SELECT from_skill_id, to_skill_id, 1, ARRAY[type], ARRAY[from_skill_id, to_skill_id]
        FROM skill.skill_edge
        WHERE type IN ('REQUIRES', 'BUILDS_ON')

        UNION ALL

        SELECT w.ancestor_id, e.to_skill_id, w.depth + 1, w.edge_types || e.type, w.path || e.to_skill_id
        FROM walk w
        JOIN skill.skill_edge e ON e.from_skill_id = w.descendant_id
        WHERE e.type IN ('REQUIRES', 'BUILDS_ON')
          AND e.to_skill_id <> ALL(w.path) -- cycle guard; skill graph is a DAG by design (doc §2.1) but guard defensively
      )
      INSERT INTO skill.skill_closure (ancestor_id, descendant_id, depth, edge_types)
      SELECT ancestor_id, descendant_id, MIN(depth), (array_agg(DISTINCT et))::text[]
      FROM walk, unnest(edge_types) AS et
      GROUP BY ancestor_id, descendant_id;
    `.execute(this.db);
  }

  /** All REQUIRES-type ancestors of a skill, for eligibility gating (§2.5 stage 2, §2.3). */
  async getRequiresAncestors(skillId: string): Promise<string[]> {
    const rows = await this.db
      .selectFrom('skill.skill_closure')
      .select('ancestor_id')
      .where('descendant_id', '=', skillId)
      .where(sql<boolean>`'REQUIRES' = ANY(edge_types)`)
      .execute();
    return rows.map((r) => r.ancestor_id);
  }

  async findBySlug(slug: string) {
    return this.db
      .selectFrom('skill.skill')
      .selectAll()
      .where('slug', '=', slug)
      .executeTakeFirst();
  }

  async listByDomain(domain: string) {
    return this.db
      .selectFrom('skill.skill')
      .selectAll()
      .where('domain', '=', domain)
      .where('status', '=', 'active')
      .execute();
  }
}
