import {
  Body,
  Controller,
  ForbiddenException,
  Get,
  Headers,
  Param,
  Post,
  Query,
} from '@nestjs/common';
import { AttemptService } from './attempt.service';
import { AttemptRepository } from './attempt.repository';
import { HintService } from './hint.service';
import { WorkspaceFileService } from './workspace-file.service';
import { AuthUser } from '../auth/auth-user.decorator';
import type { AuthClaims } from '../auth/auth.types';

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
 */
@Controller('v1/practice/attempts')
export class AttemptController {
  constructor(
    private readonly attemptService: AttemptService,
    private readonly attempts: AttemptRepository,
    private readonly hints: HintService,
    private readonly files: WorkspaceFileService,
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
  async provision(@AuthUser() auth: AuthClaims, @Param('id') id: string) {
    await this.assertOwnedByCaller(auth, id);
    return this.attemptService.provision(id);
  }

  @Post(':id/start')
  async start(@AuthUser() auth: AuthClaims, @Param('id') id: string) {
    await this.assertOwnedByCaller(auth, id);
    return this.attemptService.markStarted(id);
  }

  @Post(':id/submit')
  async submit(@AuthUser() auth: AuthClaims, @Param('id') id: string) {
    await this.assertOwnedByCaller(auth, id);
    return this.attemptService.submit(id);
  }

  @Post(':id/connect')
  async connect(@AuthUser() auth: AuthClaims, @Param('id') id: string) {
    await this.assertOwnedByCaller(auth, id);
    return this.attemptService.connect(id);
  }

  @Get(':id')
  async getById(@AuthUser() auth: AuthClaims, @Param('id') id: string) {
    await this.assertOwnedByCaller(auth, id);
    return this.attempts.findById(id);
  }

  @Get()
  async list(@AuthUser() auth: AuthClaims) {
    return this.attempts.listForUser(auth.userId);
  }

  @Get(':id/evaluation')
  async getEvaluation(@AuthUser() auth: AuthClaims, @Param('id') id: string) {
    await this.assertOwnedByCaller(auth, id);
    const score = await this.attempts.getLatestScore(id);
    return score ?? null;
  }

  @Get(':id/tasks')
  async getTasks(@AuthUser() auth: AuthClaims, @Param('id') id: string) {
    await this.assertOwnedByCaller(auth, id);
    return this.attempts.getTaskStates(id);
  }

  @Get(':id/tasks/:key/hints')
  async previewHint(
    @AuthUser() auth: AuthClaims,
    @Param('id') id: string,
    @Param('key') key: string,
  ) {
    await this.assertOwnedByCaller(auth, id);
    return this.hints.preview(id, key);
  }

  @Post(':id/tasks/:key/hints')
  async revealHint(
    @AuthUser() auth: AuthClaims,
    @Param('id') id: string,
    @Param('key') key: string,
  ) {
    await this.assertOwnedByCaller(auth, id);
    return this.hints.reveal(id, key);
  }

  @Get(':id/files')
  async listFiles(
    @AuthUser() auth: AuthClaims,
    @Param('id') id: string,
    @Query('dir') dir?: string,
  ) {
    await this.assertOwnedByCaller(auth, id);
    return this.files.list(id, dir ?? '.');
  }

  @Get(':id/files/content')
  async readFile(
    @AuthUser() auth: AuthClaims,
    @Param('id') id: string,
    @Query('path') path: string,
  ) {
    await this.assertOwnedByCaller(auth, id);
    return this.files.read(id, path);
  }

  @Post(':id/files/content')
  async writeFile(
    @AuthUser() auth: AuthClaims,
    @Param('id') id: string,
    @Query('path') path: string,
    @Body() body: { content: string },
  ) {
    await this.assertOwnedByCaller(auth, id);
    await this.files.write(id, path, body.content);
    return { ok: true };
  }

  /**
   * Doc §9.1 T3 ("cross-learner data access"): every attempt-scoped route
   * must confirm the caller's own token owns this attempt, not just that
   * the attempt id exists. Mirrors the gateway's tokenEnvID != pathEnvID
   * check for the terminal WS path (orchestrator/internal/wsgateway).
   */
  private async assertOwnedByCaller(
    auth: AuthClaims,
    attemptId: string,
  ): Promise<void> {
    const attempt = await this.attempts.findById(attemptId);
    if (
      !attempt ||
      attempt.user_id !== auth.userId ||
      attempt.tenant_id !== auth.tenantId
    ) {
      throw new ForbiddenException('attempt does not belong to caller');
    }
  }
}
