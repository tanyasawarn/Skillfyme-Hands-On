import * as fs from 'node:fs';
import * as path from 'node:path';
import { ATTEMPT_EVENT_TYPES } from './attempt-event-type';

/**
 * Phase 3 (0.8): the six Project + Cloud-account taxonomy additions.
 * Guards two things: (1) the new types are present in the union, and
 * (2) every type that documents a dedicated payload schema in
 * contracts/events.md actually has that file on disk (so a taxonomy
 * entry can't reference a schema that was never written).
 */
describe('ATTEMPT_EVENT_TYPES — Phase 3 additions', () => {
  const contractsEvents = path.resolve(
    __dirname,
    '../../../../contracts/events',
  );

  it('includes the Project + Cloud-account event types', () => {
    for (const t of [
      'MILESTONE_SUBMITTED',
      'MILESTONE_GATED',
      'DEFENCE_MESSAGE',
      'ACCOUNT_CLAIMED',
      'ACCOUNT_NUKED',
      'ACCOUNT_QUARANTINED',
    ] as const) {
      expect(ATTEMPT_EVENT_TYPES).toContain(t);
    }
  });

  it('has no duplicate entries', () => {
    expect(new Set(ATTEMPT_EVENT_TYPES).size).toBe(ATTEMPT_EVENT_TYPES.length);
  });

  it('every Phase 3 event type with a dedicated payload schema has that schema file', () => {
    const withSchema: Record<string, string> = {
      MILESTONE_GATED: 'milestone_gated.schema.json',
      DEFENCE_MESSAGE: 'defence_message.schema.json',
      ACCOUNT_CLAIMED: 'account_claimed.schema.json',
      ACCOUNT_NUKED: 'account_nuked.schema.json',
      ACCOUNT_QUARANTINED: 'account_quarantined.schema.json',
    };
    for (const [type, file] of Object.entries(withSchema)) {
      expect(ATTEMPT_EVENT_TYPES).toContain(type);
      const p = path.join(contractsEvents, file);
      expect(fs.existsSync(p)).toBe(true);
      // valid JSON Schema shape
      const schema = JSON.parse(fs.readFileSync(p, 'utf-8'));
      expect(schema.type).toBe('object');
      expect(Array.isArray(schema.required)).toBe(true);
    }
  });
});
