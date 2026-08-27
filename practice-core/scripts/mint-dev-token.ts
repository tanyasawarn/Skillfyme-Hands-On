import 'dotenv/config';
import jwt from 'jsonwebtoken';
import { isRole, Role } from '../src/modules/auth/role';

/**
 * Dev-only token minting for the shared-secret JWT auth added ahead of
 * the doc's real LMS-issued-token flow (M1.6, §8.1). Mirrors the
 * DEMO_USER_ID/DEMO_TENANT_ID stub in web/src/lib/demo-context.ts so the
 * frontend keeps working without a login UI.
 *
 * Usage: npm run mint-dev-token [role] [ttl]
 *   role defaults to "learner", ttl defaults to "12h".
 */
const DEMO_TENANT_ID = '11111111-1111-1111-1111-111111111111';
const DEMO_USER_ID = '55555555-5555-5555-5555-555555555555';

const secret = process.env.JWT_SECRET;
if (!secret) {
  console.error('JWT_SECRET is not set (see .env.example)');
  process.exit(1);
}

const requestedRole = process.argv[2] ?? Role.LEARNER;
if (!isRole(requestedRole)) {
  console.error(`unrecognized role: ${requestedRole} (expected one of: learner, author, admin)`);
  process.exit(1);
}
const role = requestedRole;
const ttl = (process.argv[3] ?? '12h') as jwt.SignOptions['expiresIn'];

const token = jwt.sign({ userId: DEMO_USER_ID, tenantId: DEMO_TENANT_ID, role }, secret, {
  expiresIn: ttl,
});

console.log(token);
