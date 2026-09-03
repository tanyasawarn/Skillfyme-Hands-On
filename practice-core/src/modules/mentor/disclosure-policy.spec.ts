import { DisclosureCeiling, resolveDisclosure } from './disclosure-policy';

describe('resolveDisclosure (PLAN.md G5 / doc §7.3)', () => {
  const base = {
    meanMastery: 0.7,
    timeStuckMinutes: 0,
  };

  it('PRODUCTION_SIM never exceeds NarrowSearch, even when stuck + low mastery', () => {
    const r = resolveDisclosure({
      ...base,
      mode: 'PRODUCTION_SIM',
      meanMastery: 0.1,
      timeStuckMinutes: 45,
    });
    expect(r.persona).toBe('SENIOR_ONCALL');
    expect(r.ceiling).toBeLessThanOrEqual(DisclosureCeiling.NarrowSearch);
    expect(r.maxCodeLines).toBe(0);
  });

  it('PROJECT caps at IdentifyArea and emits no code', () => {
    const r = resolveDisclosure({
      ...base,
      mode: 'PROJECT',
      meanMastery: 0.1,
      timeStuckMinutes: 60,
    });
    expect(r.persona).toBe('STAFF_REVIEWER');
    expect(r.ceiling).toBeLessThanOrEqual(DisclosureCeiling.IdentifyArea);
    expect(r.maxCodeLines).toBe(0);
  });

  it('GUIDED_LAB base is IdentifyArea; low mastery + stuck raises it', () => {
    const calm = resolveDisclosure({ ...base, mode: 'GUIDED_LAB' });
    expect(calm.ceiling).toBe(DisclosureCeiling.IdentifyArea);

    const struggling = resolveDisclosure({
      ...base,
      mode: 'GUIDED_LAB',
      meanMastery: 0.3,
      timeStuckMinutes: 15,
    });
    expect(struggling.ceiling).toBeGreaterThan(calm.ceiling);
  });

  it('GUIDED_LAB unlocks GiveCommand only after hint level 3', () => {
    const noHints = resolveDisclosure({
      ...base,
      mode: 'GUIDED_LAB',
      hintLevelReached: 2,
    });
    expect(noHints.ceiling).toBeLessThan(DisclosureCeiling.GiveCommand);

    const deepHints = resolveDisclosure({
      ...base,
      mode: 'GUIDED_LAB',
      hintLevelReached: 3,
    });
    expect(deepHints.ceiling).toBe(DisclosureCeiling.GiveCommand);
    expect(deepHints.maxCodeLines).toBe(5);
  });

  it('an activity override can only LOWER the ceiling, never raise it', () => {
    const lowered = resolveDisclosure({
      ...base,
      mode: 'GUIDED_LAB',
      hintLevelReached: 3, // would be GiveCommand
      activityOverride: DisclosureCeiling.ConceptOnly,
    });
    expect(lowered.ceiling).toBe(DisclosureCeiling.ConceptOnly);

    const cannotRaise = resolveDisclosure({
      ...base,
      mode: 'PRODUCTION_SIM',
      activityOverride: DisclosureCeiling.GiveCommand, // author tries to loosen
    });
    expect(cannotRaise.ceiling).toBeLessThanOrEqual(
      DisclosureCeiling.NarrowSearch,
    );
  });

  it('mayUseCanonicalPath only at IdentifyArea+ (doc §7.4: ceiling >= 2)', () => {
    expect(
      resolveDisclosure({ ...base, mode: 'PRODUCTION_SIM' })
        .mayUseCanonicalPath,
    ).toBe(false); // NarrowSearch
    expect(
      resolveDisclosure({ ...base, mode: 'GUIDED_LAB' }).mayUseCanonicalPath,
    ).toBe(true); // IdentifyArea
  });
});
