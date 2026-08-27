import { FaultRepository } from './fault.repository';

describe('FaultRepository (reads content/faults/*.yaml directly -- see class doc comment)', () => {
  const repo = new FaultRepository();

  it('extracts canonical_diagnostic_path command prefixes for a real authored fault', () => {
    const path = repo.getCanonicalDiagnosticPath('f.k8s.memory-limit-too-low');
    expect(path).toEqual([
      'kubectl get pods',
      'kubectl describe pod',
      'kubectl top pod',
      'kubectl get deploy -o yaml',
    ]);
  });

  it('returns [] for a fault id that does not exist', () => {
    expect(repo.getCanonicalDiagnosticPath('f.does-not-exist')).toEqual([]);
  });
});
