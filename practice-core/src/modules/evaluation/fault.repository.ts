import { Injectable } from '@nestjs/common';
import * as fs from 'node:fs';
import * as path from 'node:path';
import * as yaml from 'js-yaml';
import { resolveRepoRelativePath } from '../../common/repo-relative-path';

/**
 * Doc §3.4: faults are a reusable content primitive, authored as YAML in
 * content/faults/<id>.yaml -- unlike activities, there is no fault/
 * fault_version table (§4.4's schema sketch lists one, but it was never
 * built; see PLAN.md's own Phase 2 scope). Reading the file directly at
 * evaluation time is the same pattern SpecLintService/publish-all-content
 * already use for content/activities, just without the publish-to-Postgres
 * step this content type never got. Read-only, no caching -- fault count
 * is small (35 today) and evaluation is not a hot path.
 */
@Injectable()
export class FaultRepository {
  private readonly faultsDir = this.resolveFaultsDir();

  private resolveFaultsDir(): string {
    // PLAN.md Phase 4's U4: was 4 levels up, confirmed wrong (this file
    // is 5 levels from repo root once compiled to dist/src/modules/
    // evaluation) -- silently masked by always falling through to the
    // cwd fallback.
    return resolveRepoRelativePath(__dirname, 5, 'content/faults');
  }

  /**
   * Returns the fault's canonical_diagnostic_path as command-prefix
   * strings (the text before "→" in each authored step, e.g. "kubectl
   * get pods" from "kubectl get pods → notice high RESTARTS count") --
   * that prefix is what's actually matchable against real
   * COMMAND_EXECUTED text, unlike the full free-text description.
   * Returns [] if the fault file doesn't exist or has no canonical path
   * authored (never throws -- a missing/malformed fault file is a
   * content gap, not a reason to crash evaluation).
   */
  getCanonicalDiagnosticPath(faultId: string): string[] {
    const filePath = path.join(this.faultsDir, `${faultId}.yaml`);
    if (!fs.existsSync(filePath)) return [];

    try {
      const doc = yaml.load(fs.readFileSync(filePath, 'utf-8')) as {
        canonical_diagnostic_path?: string[];
      };
      const steps = doc.canonical_diagnostic_path ?? [];
      return steps.map((step) => step.split('→')[0].trim());
    } catch {
      return [];
    }
  }
}
