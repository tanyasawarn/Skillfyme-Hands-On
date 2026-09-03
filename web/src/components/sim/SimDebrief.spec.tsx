import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { SimDebrief } from './SimDebrief';
import type { ActivitySpec, AttemptEvaluation, AttemptTaskState } from '@/lib/api-client';

const spec: ActivitySpec = {
  meta: { title: 'INC-5809' },
  objectives: ['Diagnose the selector mismatch', 'Restore egress-proxy access', 'Write the incident note'],
  faults: [
    { id: 'f.k8s.wrong-service-selector', apply_at: 'T0' },
    { id: 'f.cloud.egress-proxy-allowlist-too-strict', apply_at: 'T+12' },
  ],
  reference_solution: { repo_path: 'solutions/sim.k8s.checkout-network-incident/', visibility: 'AFTER_PASS_OR_EXHAUST' },
};

const evaluation: AttemptEvaluation = {
  final_score: '0.82',
  passed: true,
  profile_version_id: 'sp.production-sim.default',
  breakdown_jsonb: {
    troubleshooting: { value: 0.85, weight: 0.5, explanation: {} },
    technical_implementation: { value: 0.8, weight: 0.3, explanation: {} },
  },
  penalties_jsonb: { overtime: 0.05, blast_radius: 0 },
  computed_at: '2026-01-01T00:40:00.000Z',
};

const tasks: AttemptTaskState[] = [
  { task_key: 't1', status: 'PASSED', attempts_count: 2, hints_used_max_level: 1, skipped: false },
  { task_key: 't2', status: 'FAILED', attempts_count: 3, hints_used_max_level: 0, skipped: false },
];

describe('SimDebrief', () => {
  it('shows a "what was actually wrong" entry per fault, with the escalated flag', () => {
    render(<SimDebrief spec={spec} evaluation={evaluation} tasks={tasks} />);
    expect(screen.getByText('What was actually wrong')).toBeInTheDocument();
    expect(screen.getByText('wrong service selector')).toBeInTheDocument();
    expect(screen.getByText('egress proxy allowlist too strict')).toBeInTheDocument();
    expect(screen.getByText('escalated T+12')).toBeInTheDocument();
  });

  it('renders per-fault notes when provided', () => {
    render(
      <SimDebrief
        spec={spec}
        evaluation={evaluation}
        faultNotes={{ 'f.k8s.wrong-service-selector': 'The Service selector was changed to a value matching no pods.' }}
      />,
    );
    expect(screen.getByText(/changed to a value matching no pods/)).toBeInTheDocument();
  });

  it('shows the solution path from objectives + the reference solution location', () => {
    render(<SimDebrief spec={spec} evaluation={evaluation} tasks={tasks} />);
    expect(screen.getByText('Diagnose the selector mismatch')).toBeInTheDocument();
    expect(screen.getByText('solutions/sim.k8s.checkout-network-incident/')).toBeInTheDocument();
    expect(screen.getByText(/AFTER_PASS_OR_EXHAUST/)).toBeInTheDocument();
  });

  it('shows the score breakdown, non-zero penalties only, and the task timeline', () => {
    render(<SimDebrief spec={spec} evaluation={evaluation} tasks={tasks} />);
    expect(screen.getByText('troubleshooting')).toBeInTheDocument();
    // overtime penalty shown, blast_radius (0) filtered out
    expect(screen.getByText(/overtime/)).toBeInTheDocument();
    expect(screen.queryByText(/blast radius/)).not.toBeInTheDocument();
    // task rows with hint annotation
    expect(screen.getByText('t1')).toBeInTheDocument();
    expect(screen.getByText(/passed · hint L1/)).toBeInTheDocument();
    expect(screen.getByText('t2')).toBeInTheDocument();
  });

  it('renders the pass/fail header', () => {
    render(<SimDebrief spec={spec} evaluation={{ ...evaluation, passed: false }} tasks={tasks} />);
    expect(screen.getByText(/Not resolved/)).toBeInTheDocument();
  });
});
