import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { SimShell } from './SimShell';
import type { ActivitySpec, Attempt } from '@/lib/api-client';

// The hint panel hits the API on mount; stub it so these tests stay unit-scoped.
vi.mock('@/lib/api-client', async (orig) => {
  const actual = await orig<typeof import('@/lib/api-client')>();
  return {
    ...actual,
    api: {
      ...actual.api,
      previewHint: vi.fn().mockResolvedValue(null),
      revealHint: vi.fn(),
      submitArtifact: vi.fn(),
    },
  };
});

function wrap(ui: React.ReactNode) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(<QueryClientProvider client={qc}>{ui}</QueryClientProvider>);
}

const baseAttempt: Attempt = {
  id: 'aaaaaaaa-1111-2222-3333-444444444444',
  tenant_id: 't',
  user_id: 'u',
  activity_id: 'act',
  activity_version_id: 'av',
  mode: 'PRODUCTION_SIM',
  status: 'IN_PROGRESS',
  environment_id: 'env-1',
  created_at: '2026-01-01T00:00:00.000Z',
  started_at: '2026-01-01T00:00:00.000Z',
  submitted_at: null,
  completed_at: null,
  version: 1,
};

const spec: ActivitySpec = {
  meta: { title: 'INC-5809: checkout unreachable', summary: 'Two independent causes.', difficulty_level: 'L2', estimated_minutes: 25 },
  objectives: ['Fix the Service', 'Fix the NetworkPolicy'],
  tasks: [
    { key: 't1', title: 'Service has zero endpoints', required: true, instructions_md: 'Diagnose the selector.' },
  ],
  faults: [
    { id: 'f.k8s.wrong-service-selector', apply_at: 'T0' },
    { id: 'f.cloud.egress-proxy-allowlist-too-strict', apply_at: 'T+12' },
  ],
  artifacts_required: [{ key: 'incident-note', type: 'MARKDOWN', rubric: 'rub.incident-note.v2' }],
};

beforeEach(() => {
  vi.setSystemTime(new Date('2026-01-01T00:05:00.000Z')); // 5 min in
});

describe('SimShell', () => {
  it('renders the incident ticket from meta', () => {
    wrap(<SimShell attemptId="a" attempt={baseAttempt} spec={spec} />);
    expect(screen.getByText('INC-5809: checkout unreachable')).toBeInTheDocument();
    expect(screen.getByText('Two independent causes.')).toBeInTheDocument();
    expect(screen.getByText('L2')).toBeInTheDocument();
  });

  it('shows the SLA timer with elapsed and budget', () => {
    wrap(<SimShell attemptId="a" attempt={baseAttempt} spec={spec} />);
    expect(screen.getByText('SLA timer')).toBeInTheDocument();
    // 5 min elapsed / 25 min budget
    expect(screen.getByText(/05:00/)).toBeInTheDocument();
    expect(screen.getByText(/25:00/)).toBeInTheDocument();
  });

  it('lists a symptom per fault, marking the T+N one as not-yet-escalated at 5 min', () => {
    wrap(<SimShell attemptId="a" attempt={baseAttempt} spec={spec} />);
    expect(screen.getByText('System symptoms')).toBeInTheDocument();
    expect(screen.getByText('wrong service selector')).toBeInTheDocument();
    expect(screen.getByText(/escalates T\+12/)).toBeInTheDocument();
  });

  it('shows the "heads up" escalation banner while a T+N fault is still pending', () => {
    wrap(<SimShell attemptId="a" attempt={baseAttempt} spec={spec} />);
    expect(screen.getByText(/expected to escalate at/)).toBeInTheDocument();
  });

  it('renders the incident-note editor with the 5-section template', () => {
    wrap(<SimShell attemptId="a" attempt={baseAttempt} spec={spec} />);
    const editor = screen.getByLabelText('Incident note editor') as HTMLTextAreaElement;
    expect(editor.value).toMatch(/## Root cause/);
    expect(editor.value).toMatch(/## Prevention/);
    expect(screen.getByText('rub.incident-note.v2')).toBeInTheDocument();
  });

  it('renders the task rows', () => {
    wrap(<SimShell attemptId="a" attempt={baseAttempt} spec={spec} />);
    expect(screen.getByText('Service has zero endpoints')).toBeInTheDocument();
    expect(screen.getByText('Diagnose the selector.')).toBeInTheDocument();
  });
});

describe('SimShell — after the escalation time', () => {
  beforeEach(() => vi.setSystemTime(new Date('2026-01-01T00:15:00.000Z'))); // 15 min in, past T+12

  it('shows the fired-escalation banner and marks the symptom escalated', () => {
    wrap(<SimShell attemptId="a" attempt={baseAttempt} spec={spec} />);
    expect(screen.getByText(/a second failure/)).toBeInTheDocument();
    expect(screen.getAllByText('escalated').length).toBeGreaterThan(0);
  });
});
