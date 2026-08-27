import {
  Body,
  Controller,
  Get,
  Headers,
  Param,
  Post,
  Query,
  UseGuards,
} from '@nestjs/common';
import { AttemptService } from './attempt.service';
import { AttemptRepository } from './attempt.repository';
import { HintService } from './hint.service';
import { WorkspaceFileService } from './workspace-file.service';
import { ArtifactService } from '../evaluation/artifact.service';
import { AuthUser } from '../auth/auth-user.decorator';
import type { AuthClaims } from '../auth/auth.types';
import { AttemptOwnershipGuard } from './attempt-ownership.guard';

/**
 * Doc §8.3 learner surface, Phase 1 subset. The doc's literal route
 * syntax is `/attempts/{id}:submit` (colon-action, Google AIP style), but
 * the NestJS 11 / path-to-regexp version pinned here rejects a literal
 * `:` after a route param (`:id::submit` fails to parse as a route at
 * startup -- confirmed by booting the app). Using `/attempts/{id}/submit`
 * instead: same resource-oriented intent (§8.3 principle 44: "commands
 * that are not CRUD get explicit action sub-resources"), just a `/`
 * separator instead of `:` to stay compatible with this router version.
 *   POST /v1/practice/attempts                 (create, 202-style: returns CREATED status)
 *   POST /v1/practice/attempts/{id}/provision
 *   POST /v1/practice/attempts/{id}/start
 *   POST /v1/practice/attempts/{id}/check      (learner-triggered validation, doc §6.3 -- no state change)
 *   POST /v1/practice/attempts/{id}/submit
 *   POST /v1/practice/attempts/{id}/connect     (mints terminal WS session token, doc §5.4)
 *   GET  /v1/practice/attempts?user_id
 *   GET  /v1/practice/attempts/{id}
 *   GET  /v1/practice/attempts/{id}/evaluation
 *   GET  /v1/practice/attempts/{id}/tasks
 *   GET  /v1/practice/attempts/{id}/tasks/{key}/hints   (preview, doc §7.5 step 38 -- no side effect)
 *   POST /v1/practice/attempts/{id}/tasks/{key}/hints   (reveal + commit, doc §7.5 step 39)
 *   GET  /v1/practice/attempts/{id}/files?dir=          (Monaco file tree, doc §8.5 -- client-side editor's file API)
 *   GET  /v1/practice/attempts/{id}/files/content?path= (read one file's content)
 *   POST /v1/practice/attempts/{id}/files/content?path= (write one file's content)
 *   POST /v1/practice/attempts/{id}/artifacts/{key}     (submit a required written artifact, doc §3.3/§6.5)
 */
@Controller('v1/practice/attempts')
export class AttemptController {
  constructor(
    private readonly attemptService: AttemptService,
    private readonly attempts: AttemptRepository,
    private readonly hints: HintService,
    private readonly files: WorkspaceFileService,
    private readonly artifacts: ArtifactService,
  ) {}

  @Post()
  async create(
    @AuthUser() auth: AuthClaims,
    @Body() body: { activity_version_id: string },
    @Headers('idempotency-key') idempotencyKey?: string,
  ) {
    return this.attemptService.createAttempt({
      tenantId: auth.tenantId,
      userId: auth.userId,
      activityVersionId: body.activity_version_id,
      idempotencyKey,
    });
  }

  @Post(':id/provision')
  @UseGuards(AttemptOwnershipGuard)
  async provision(@Param('id') id: string) {
    // Reactivation entry point: revised lifecycle requirement §5 --
    // "click Start Lab" on a SUSPENDED or CACHED attempt must resume it,
    // not create a new instance. reactivate() does the one extra
    // SUSPENDED/CACHED -> CREATED hop before the normal provision path
    // applies, and is a no-op for every attempt that was never
    // suspended/cached, so this is safe to call unconditionally rather
    // than branching on status here too -- reactivate()'s own status
    // guard is the idempotency check requirement §8 asks for.
    await this.attemptService.reactivate(id);
    return this.attemptService.provision(id);
  }

  @Post(':id/start')
  @UseGuards(AttemptOwnershipGuard)
  async start(@Param('id') id: string) {
    return this.attemptService.markStarted(id);
  }

  /**
   * Doc §6.3 learner-triggered validation. Runs the validator set and
   * returns per-task pass/fail without ending the attempt -- the "Check
   * my work" feedback loop the workspace needs so a learner isn't forced
   * to submit blind and hit a dead-end result screen.
   */
  @Post(':id/check')
  @UseGuards(AttemptOwnershipGuard)
  async check(@Param('id') id: string) {
    return this.attemptService.checkWork(id);
  }

  @Post(':id/submit')
  @UseGuards(AttemptOwnershipGuard)
  async submit(@Param('id') id: string) {
    return this.attemptService.submit(id);
  }

  @Post(':id/connect')
  @UseGuards(AttemptOwnershipGuard)
  async connect(@Param('id') id: string) {
    await this.attempts.touch(id);
    return this.attemptService.connect(id);
  }

  @Get(':id')
  @UseGuards(AttemptOwnershipGuard)
  async getById(@Param('id') id: string) {
    return this.attempts.findById(id);
  }

  @Get()
  async list(@AuthUser() auth: AuthClaims) {
    return this.attempts.listForUser(auth.userId);
  }

  @Get(':id/evaluation')
  @UseGuards(AttemptOwnershipGuard)
  async getEvaluation(@Param('id') id: string) {
    const score = await this.attempts.getLatestScore(id);
    return score ?? null;
  }

  @Get(':id/tasks')
  @UseGuards(AttemptOwnershipGuard)
  async getTasks(@Param('id') id: string) {
    return this.attempts.getTaskStates(id);
  }

  @Get(':id/tasks/:key/hints')
  @UseGuards(AttemptOwnershipGuard)
  async previewHint(@Param('id') id: string, @Param('key') key: string) {
    return this.hints.preview(id, key);
  }

  @Post(':id/tasks/:key/hints')
  @UseGuards(AttemptOwnershipGuard)
  async revealHint(@Param('id') id: string, @Param('key') key: string) {
    return this.hints.reveal(id, key);
  }

  @Get(':id/files')
  @UseGuards(AttemptOwnershipGuard)
  async listFiles(@Param('id') id: string, @Query('dir') dir?: string) {
    return this.files.list(id, dir ?? '.');
  }

  @Get(':id/files/content')
  @UseGuards(AttemptOwnershipGuard)
  async readFile(@Param('id') id: string, @Query('path') path: string) {
    return this.files.read(id, path);
  }

  @Post(':id/files/content')
  @UseGuards(AttemptOwnershipGuard)
  async writeFile(
    @Param('id') id: string,
    @Query('path') path: string,
    @Body() body: { content: string },
  ) {
    await this.files.write(id, path, body.content);
    await this.attempts.touch(id);
    return { ok: true };
  }

  /**
   * Doc §3.3/§6.5: submits a required written artifact (e.g. an incident
   * note). Grading is not triggered here -- submit can happen any time
   * before the attempt is submitted, but grading needs the attempt's
   * final validator results (doc rule 34's ground truth), so it runs
   * from EvaluationService.evaluate() at submit time instead.
   */
  @Post(':id/artifacts/:key')
  @UseGuards(AttemptOwnershipGuard)
  async submitArtifact(
    @Param('id') id: string,
    @Param('key') key: string,
    @Body() body: { content: string },
  ) {
    return this.artifacts.submit(id, key, body.content);
  }
}
