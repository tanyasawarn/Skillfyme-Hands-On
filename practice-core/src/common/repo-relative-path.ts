import * as fs from 'node:fs';
import * as path from 'node:path';

/**
 * PLAN.md Phase 4's U4: the "resolve a repo-root-relative directory,
 * trying __dirname first then process.cwd() as a fallback" pattern was
 * copy-pasted 4 times (`base-grpc-client.ts`, `spec-lint.service.ts`,
 * `fault.repository.ts`, `rubric.repository.ts`) -- and 3 of the 4
 * copies had the same real bug, not just style duplication: `src/common/
 * base-grpc-client.ts` correctly used `../../../../` (4 levels up from
 * its own compiled `dist/src/common/` location reaches the real repo
 * root), but the other 3 -- all one directory deeper, under
 * src/modules/ (compiled to dist/src/modules/) -- were copy-
 * pasted with the SAME `../../../../` count, which resolves to a
 * nonexistent `practice-core/contracts`/`practice-core/content/*`
 * path, confirmed live via a direct Node.js path.resolve() check
 * against the real dist/ layout. This has been silently masked in
 * practice by every one of them falling through to their `fromCwd`
 * fallback on every real call (works today only because the app always
 * happens to run with `practice-core/` as cwd) -- fragile, not fixed,
 * the exact kind of bug this extraction exists to close for good.
 *
 * `dirnameUpLevels` is required, not defaulted, precisely because
 * getting this number right per caller (based on that caller's own
 * real depth under `dist/src/`) is what the 3 broken copies got wrong
 * -- a shared function with a silently-wrong default would just move
 * the same bug to one place instead of fixing it.
 */
export function resolveRepoRelativePath(
  callerDirname: string,
  dirnameUpLevels: number,
  repoRelativeSubpath: string,
): string {
  const upSegments = Array(dirnameUpLevels).fill('..');
  const fromDirname = path.resolve(
    callerDirname,
    ...upSegments,
    repoRelativeSubpath,
  );
  if (fs.existsSync(fromDirname)) return fromDirname;

  const fromCwd = path.resolve(process.cwd(), '..', repoRelativeSubpath);
  if (fs.existsSync(fromCwd)) return fromCwd;

  throw new Error(
    `${repoRelativeSubpath} not found from ${fromDirname} or ${fromCwd}`,
  );
}
