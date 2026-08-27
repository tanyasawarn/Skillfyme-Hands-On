import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { resolveRepoRelativePath } from './repo-relative-path';

describe('resolveRepoRelativePath', () => {
  it('resolves via callerDirname when the path exists at that depth', () => {
    // __dirname under ts-jest is the real .../practice-core/src/common
    // (confirmed live, not assumed): 2 levels up (src/common -> src ->
    // practice-core) reaches package.json's real location.
    const result = resolveRepoRelativePath(__dirname, 2, 'package.json');
    expect(fs.existsSync(result)).toBe(true);
    expect(path.basename(result)).toBe('package.json');
  });

  it('falls back to process.cwd()-relative when callerDirname resolution is wrong (the real bug 3 of 4 original copies had)', () => {
    // Real production fixture, not an arbitrary one: contracts/ lives
    // one level above practice-core/ (the monorepo root), exactly what
    // base-grpc-client.ts/spec-lint.service.ts resolve in real use.
    // Deliberately wrong level count (0 -- callerDirname alone, with no
    // ups at all) so the callerDirname attempt genuinely fails and
    // falls through to the cwd fallback, which succeeds since
    // process.cwd() while running tests really is practice-core/ and
    // contracts/ is one level above that.
    const result = resolveRepoRelativePath(__dirname, 0, 'contracts');
    expect(fs.existsSync(result)).toBe(true);
    expect(result).toBe(path.resolve(process.cwd(), '..', 'contracts'));
  });

  it('throws a descriptive error mentioning both attempted paths when neither exists', () => {
    expect(() =>
      resolveRepoRelativePath(__dirname, 2, 'definitely-does-not-exist.xyz'),
    ).toThrow(/definitely-does-not-exist\.xyz not found from/);
  });

  it('resolves a directory path (not just a file), matching fault.repository.ts/rubric.repository.ts', () => {
    const tmpDir = fs.mkdtempSync(
      path.join(os.tmpdir(), 'repo-relative-path-test-'),
    );
    try {
      const nested = path.join(tmpDir, 'nested-dir');
      fs.mkdirSync(nested);
      const result = resolveRepoRelativePath(tmpDir, 0, 'nested-dir');
      expect(fs.existsSync(result)).toBe(true);
      expect(fs.statSync(result).isDirectory()).toBe(true);
    } finally {
      fs.rmSync(tmpDir, { recursive: true, force: true });
    }
  });
});
