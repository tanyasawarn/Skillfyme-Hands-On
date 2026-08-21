import { NestFactory } from '@nestjs/core';
import { AppModule } from './app.module';

async function bootstrap() {
  const app = await NestFactory.create(AppModule);
  // Phase 1 dev-only CORS: the web app (Next.js, localhost:3000) calls
  // this API (localhost:3001) directly from the browser. Tighten this to
  // an explicit allowlist read from config once there's a real deployed
  // frontend origin -- wildcard is fine for local dev only.
  app.enableCors({
    origin: process.env.CORS_ORIGIN ?? 'http://localhost:3000',
  });
  await app.listen(process.env.PORT ?? 3000);
}
bootstrap();
