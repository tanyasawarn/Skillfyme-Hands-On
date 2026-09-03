import { describe, it, expect } from 'vitest';
import {
  parseApplyAt,
  faultLabel,
  buildTicket,
  buildSymptoms,
  computeClock,
  formatDuration,
  isProductionSim,
} from './sim';
import type { ActivitySpec } from './api-client';

describe('parseApplyAt', () => {
  it('T0 -> 0', () => expect(parseApplyAt('T0')).toBe(0));
  it('T+12 -> 12', () => expect(parseApplyAt('T+12')).toBe(12));
  it('T+120 -> 120', () => expect(parseApplyAt('T+120')).toBe(120));
  it('whitespace tolerated', () => expect(parseApplyAt('  T+5 ')).toBe(5));
  it('garbage -> 0', () => expect(parseApplyAt('later')).toBe(0));
});

describe('faultLabel', () => {
  it('takes the last dotted segment and de-kebabs it', () => {
    expect(faultLabel('f.k8s.wrong-service-selector')).toBe('wrong service selector');
    expect(faultLabel('f.istio.mtls-mode-mismatch')).toBe('mtls mode mismatch');
  });
  it('handles a no-dot id', () => expect(faultLabel('plainid')).toBe('plainid'));
});

describe('buildTicket', () => {
  it('uses meta.title/summary when present', () => {
    const spec: ActivitySpec = {
      meta: { title: 'INC-42: checkout down', summary: 'It broke.', difficulty_level: 'L3' },
    };
    expect(buildTicket(spec)).toEqual({
      title: 'INC-42: checkout down',
      body: 'It broke.',
      difficulty: 'L3',
    });
  });
  it('falls back when meta is absent', () => {
    const t = buildTicket({});
    expect(t.title).toBe('Production incident');
    expect(t.body).toMatch(/Diagnose the root cause/);
    expect(t.difficulty).toBeNull();
  });
});

describe('buildSymptoms', () => {
  const spec: ActivitySpec = {
    faults: [
      { id: 'f.k8s.b', apply_at: 'T+15' },
      { id: 'f.k8s.a', apply_at: 'T0' },
      { id: 'f.k8s.c', apply_at: 'T+5' },
    ],
  };
  it('one symptom per fault, sorted T0 first then by escalation time', () => {
    const s = buildSymptoms(spec);
    expect(s.map((x) => x.faultId)).toEqual(['f.k8s.a', 'f.k8s.c', 'f.k8s.b']);
    expect(s[0].appliesAtMinutes).toBe(0);
    expect(s[2].appliesAtMinutes).toBe(15);
  });
  it('empty when no faults', () => expect(buildSymptoms({})).toEqual([]));
});

describe('computeClock', () => {
  const spec: ActivitySpec = {
    meta: { estimated_minutes: 30 },
    faults: [
      { id: 'f.a', apply_at: 'T0' },
      { id: 'f.b', apply_at: 'T+10' },
      { id: 'f.c', apply_at: 'T+25' },
    ],
  };
  const started = '2026-01-01T00:00:00.000Z';
  const startMs = Date.parse(started);

  it('elapsed + budget + fraction at t=6min', () => {
    const c = computeClock(spec, { started_at: started }, startMs + 6 * 60_000);
    expect(c.elapsedMs).toBe(6 * 60_000);
    expect(c.budgetMs).toBe(30 * 60_000);
    expect(c.fraction).toBeCloseTo(0.2);
    expect(c.overSla).toBe(false);
  });

  it('nextEscalation is the first pending T+N; fired list grows over time', () => {
    const at12 = computeClock(spec, { started_at: started }, startMs + 12 * 60_000);
    expect(at12.firedEscalations.map((s) => s.faultId)).toEqual(['f.b']);
    expect(at12.nextEscalation?.faultId).toBe('f.c');

    const at26 = computeClock(spec, { started_at: started }, startMs + 26 * 60_000);
    expect(at26.firedEscalations.map((s) => s.faultId)).toEqual(['f.b', 'f.c']);
    expect(at26.nextEscalation).toBeNull();
  });

  it('overSla true past the budget; fraction clamps at 1.5', () => {
    const c = computeClock(spec, { started_at: started }, startMs + 60 * 60_000);
    expect(c.overSla).toBe(true);
    expect(c.fraction).toBe(1.5);
  });

  it('no budget when estimated_minutes is unset', () => {
    const c = computeClock({ faults: spec.faults }, { started_at: started }, startMs + 5 * 60_000);
    expect(c.budgetMs).toBeNull();
    expect(c.fraction).toBe(0);
    expect(c.overSla).toBe(false);
  });

  it('treats a null started_at as "just started" (no negative elapsed)', () => {
    const c = computeClock(spec, { started_at: null }, startMs);
    expect(c.elapsedMs).toBe(0);
  });
});

describe('formatDuration', () => {
  it('mm:ss', () => {
    expect(formatDuration(0)).toBe('00:00');
    expect(formatDuration(65_000)).toBe('01:05');
    expect(formatDuration(3_599_000)).toBe('59:59');
  });
});

describe('isProductionSim', () => {
  it('only PRODUCTION_SIM', () => {
    expect(isProductionSim('PRODUCTION_SIM')).toBe(true);
    expect(isProductionSim('GUIDED_LAB')).toBe(false);
    expect(isProductionSim(undefined)).toBe(false);
    expect(isProductionSim(null)).toBe(false);
  });
});
