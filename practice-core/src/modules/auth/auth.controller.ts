import { BadRequestException, Controller, Post, Body } from '@nestjs/common';
import { JwtService } from '@nestjs/jwt';
import { Throttle } from '@nestjs/throttler';
import { Public } from './public.decorator';
import { isRole, Role } from './role';

const DEMO_TENANT_ID = '11111111-1111-1111-1111-111111111111';
const DEMO_USER_ID = '55555555-5555-5555-5555-555555555555';

export interface DevLoginRequest {
  role?: string;
}

export interface DevLoginResponse {
  token: string;
  expiresIn: string;
}

/**
 * HTTP counterpart to scripts/mint-dev-token.ts -- that CLI script mints
 * a token for a human running it locally; this endpoint is what the
 * frontend actually calls, since a browser can't invoke a CLI script.
 * Same demo user/tenant, same shared-secret signer (AuthModule's
 * JwtService). Dev-only by name and by design: no password, no real
 * identity check -- this is the honest placeholder ahead of the real
 * LMS-issued-token flow (auth.types.ts's own doc comment), not
 * production auth. Marked @Public() since a login endpoint can't itself
 * require the token it's about to issue.
 */
@Controller('v1/auth')
export class AuthController {
  constructor(private readonly jwt: JwtService) {}

  @Public()
  // Tighter than the app-wide default (app.module.ts: 100 req/60s) --
  // this endpoint mints a real, valid credential on every successful
  // call with no password check, making it the single highest-value
  // target in this app for a naive rate-limit bypass attempt. 5 req/60s
  // per client IP is generous for the one real caller (the frontend's
  // auth-token.ts, which only calls this on token expiry/first load,
  // not per-request) while making a mint-tokens-in-a-loop attempt
  // meaningfully slow.
  @Throttle({ default: { ttl: 60_000, limit: 5 } })
  @Post('dev-login')
  async devLogin(@Body() body: DevLoginRequest): Promise<DevLoginResponse> {
    const requested = body.role ?? Role.LEARNER;
    if (!isRole(requested)) {
      throw new BadRequestException(`unrecognized role: ${requested}`);
    }
    const role = requested;
    const expiresIn = '12h';
    const token = await this.jwt.signAsync(
      { userId: DEMO_USER_ID, tenantId: DEMO_TENANT_ID, role },
      { expiresIn },
    );
    return { token, expiresIn };
  }
}
