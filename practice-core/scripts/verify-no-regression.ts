import { NestFactory } from '@nestjs/core';
import { AppModule } from '../src/app.module';
import { GrpcValidatorExecutor } from '../src/modules/evaluation/grpc-validator-executor';

async function main() {
  const environmentId = process.argv[2];
  const snapshotKey = process.argv[3] ?? 'test.baseline';
  if (!environmentId) {
    console.error('usage: verify-no-regression.ts <environment_id> [snapshot_key]');
    process.exit(1);
  }

  const app = await NestFactory.createApplicationContext(AppModule);
  const executor = app.get(GrpcValidatorExecutor);

  // NO_REGRESSION routes to CaptureBaseline/CheckRegression, not
  // ExecValidator -- those two RPCs are deliberately out of scope for the
  // ownership-check fix (PLAN_RPC_AUTHZ.md Section 4d), so this script's
  // attemptId is never actually read; a fixed placeholder just satisfies
  // ValidatorExecutor.execute()'s signature.
  const result = await executor.execute(environmentId, 'debug-script-no-attempt', {
    id: 'v.test-no-regression',
    type: 'NO_REGRESSION',
    run: snapshotKey,
    expect: {},
    weight: 1,
  });
  console.log('RESULT:', JSON.stringify(result, null, 2));

  await app.close();
}
main().catch((e) => {
  console.error(e);
  process.exit(1);
});
