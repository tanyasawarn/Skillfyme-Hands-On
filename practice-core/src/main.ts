import { NestFactory } from '@nestjs/core';
import helmet from 'helmet';
import { AppModule } from './app.module';
import { AllExceptionsFilter } from './common/all-exceptions.filter';

async function bootstrap() {
  const app = await NestFactory.create(AppModule);
  // Phase 3 standardization: this is a pure JSON API (no HTML rendering,
  // no cookies -- auth is a bearer JWT in the Authorization header, see
  // web/src/lib/auth-token.ts), so helmet's Content-Security-Policy
  // module is inapplicable and disabled (CSP governs what a browser
  // renders in an HTML document; this server never sends one -- a
  // default CSP here would be dead weight, not real protection). The
  // headers that DO matter for a JSON API stay on by default:
  // X-Content-Type-Options: nosniff (a response can't be MIME-sniffed
  // into something a browser executes), X-Frame-Options / COOP/CORP
  // (defense in depth), X-Powered-By removed (don't advertise Express).
  app.use(helmet({ contentSecurityPolicy: false }));
  // Phase 1 dev-only CORS: the web app (Next.js, localhost:3000) calls
  // this API (localhost:3001) directly from the browser. Tighten this to
  // an explicit allowlist read from config once there's a real deployed
  // frontend origin -- wildcard is fine for local dev only.
  app.enableCors({
    origin: process.env.CORS_ORIGIN ?? 'http://localhost:3000',
  });
  // Phase 3 standardization: one place controlling error response shape
  // and error logging for the whole app -- see all-exceptions.filter.ts's
  // own doc comment for exactly what gap this closes (unhandled
  // exceptions, e.g. Kysely's executeTakeFirstOrThrow misses, previously
  // fell through to Nest's default handler with no guarantee against
  // leaking internal error detail to the client).
  app.useGlobalFilters(new AllExceptionsFilter());
  await app.listen(process.env.PORT ?? 3000);
}
bootstrap();
