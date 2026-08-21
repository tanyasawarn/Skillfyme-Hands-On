import type { Kysely } from 'kysely';
import type { Database } from '../../src/db/schema';
import { SkillRepository } from '../../src/modules/skill/skill.repository';
import { createTestDb, truncateAll } from './test-db';

describe('SkillRepository (integration, real Postgres)', () => {
  let db: Kysely<Database>;
  let repo: SkillRepository;

  beforeAll(() => {
    db = createTestDb();
    repo = new SkillRepository(db);
  });

  afterAll(async () => {
    await db.destroy();
  });

  beforeEach(async () => {
    await truncateAll(db);
  });

  it('computes transitive REQUIRES closure via the recursive CTE (doc §2.2, §2.3 example chain)', async () => {
    // Doc §2.3 example chain (subset):
    // linux.cli --REQUIRES--> docker.basics --REQUIRES--> docker.networking --REQUIRES--> k8s.core --REQUIRES--> k8s.deployments
    const linuxCli = await repo.createSkill({
      slug: 'linux.cli',
      name: 'Linux CLI',
      domain: 'linux',
    });
    const dockerBasics = await repo.createSkill({
      slug: 'docker.basics',
      name: 'Docker Basics',
      domain: 'docker',
    });
    const dockerNetworking = await repo.createSkill({
      slug: 'docker.networking',
      name: 'Docker Networking',
      domain: 'docker',
    });
    const k8sCore = await repo.createSkill({
      slug: 'k8s.core',
      name: 'K8s Core',
      domain: 'k8s',
    });
    const k8sDeployments = await repo.createSkill({
      slug: 'k8s.deployments',
      name: 'K8s Deployments',
      domain: 'k8s',
    });

    await repo.addEdge({
      fromSkillId: linuxCli.id,
      toSkillId: dockerBasics.id,
      type: 'REQUIRES',
    });
    await repo.addEdge({
      fromSkillId: dockerBasics.id,
      toSkillId: dockerNetworking.id,
      type: 'REQUIRES',
    });
    await repo.addEdge({
      fromSkillId: dockerNetworking.id,
      toSkillId: k8sCore.id,
      type: 'REQUIRES',
    });
    await repo.addEdge({
      fromSkillId: k8sCore.id,
      toSkillId: k8sDeployments.id,
      type: 'REQUIRES',
    });

    await repo.rebuildClosure();

    const ancestors = await repo.getRequiresAncestors(k8sDeployments.id);
    expect(new Set(ancestors)).toEqual(
      new Set([linuxCli.id, dockerBasics.id, dockerNetworking.id, k8sCore.id]),
    );
  });

  it('does not treat SIBLING edges as prerequisite-closure paths', async () => {
    const a = await repo.createSkill({
      slug: 'skill.a',
      name: 'A',
      domain: 'test',
    });
    const b = await repo.createSkill({
      slug: 'skill.b',
      name: 'B',
      domain: 'test',
    });
    await repo.addEdge({ fromSkillId: a.id, toSkillId: b.id, type: 'SIBLING' });
    await repo.rebuildClosure();

    const ancestors = await repo.getRequiresAncestors(b.id);
    expect(ancestors).toEqual([]);
  });

  it('rebuildClosure is idempotent and safe to call repeatedly (doc §2.2: synchronous on publish)', async () => {
    const a = await repo.createSkill({
      slug: 'skill.a',
      name: 'A',
      domain: 'test',
    });
    const b = await repo.createSkill({
      slug: 'skill.b',
      name: 'B',
      domain: 'test',
    });
    await repo.addEdge({
      fromSkillId: a.id,
      toSkillId: b.id,
      type: 'REQUIRES',
    });

    await repo.rebuildClosure();
    await repo.rebuildClosure();
    await repo.rebuildClosure();

    const ancestors = await repo.getRequiresAncestors(b.id);
    expect(ancestors).toEqual([a.id]);
  });

  it('guards against cycles instead of infinite-looping (defensive, doc §2.1 assumes a DAG)', async () => {
    const a = await repo.createSkill({
      slug: 'skill.a',
      name: 'A',
      domain: 'test',
    });
    const b = await repo.createSkill({
      slug: 'skill.b',
      name: 'B',
      domain: 'test',
    });
    await repo.addEdge({
      fromSkillId: a.id,
      toSkillId: b.id,
      type: 'REQUIRES',
    });
    await repo.addEdge({
      fromSkillId: b.id,
      toSkillId: a.id,
      type: 'REQUIRES',
    }); // cycle

    await expect(repo.rebuildClosure()).resolves.not.toThrow();
  }, 10_000);
});
