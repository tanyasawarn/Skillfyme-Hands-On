import { NestFactory } from '@nestjs/core';
import { AppModule } from '../src/app.module';
import { GrpcOrchestratorClient } from '../src/modules/attempt/grpc-orchestrator.client';

async function main() {
  const environmentId = process.argv[2];
  const attemptId = process.argv[3];
  if (!environmentId || !attemptId) {
    // attempt_id is required now that InjectFault verifies the caller's
    // attempt actually owns environment_id (PHASE2_CLOSEOUT.md's access-
    // control gap, closed) -- find the real value with:
    //   psql ... -c "SELECT attempt_id FROM env.environment WHERE id = '<environment_id>'"
    console.error('usage: verify-inject-fault.ts <environment_id> <attempt_id>');
    process.exit(1);
  }

  const app = await NestFactory.createApplicationContext(AppModule);
  const client = app.get(GrpcOrchestratorClient);

  // A ConfigMap that doesn't exist in this environment's namespace --
  // proves the real gRPC round trip (client -> orchestrator ->
  // faultinjection.Apply) without mutating live cluster state, since the
  // handler's own not-found path returns applied=false cleanly.
  const result = await client.injectFault({
    environmentId,
    faultId: 'f.k8s.configmap-key-renamed',
    params: { configmap: 'does-not-exist', old_key: 'a', new_key: 'b' },
    attemptId,
  });
  console.log('RESULT:', JSON.stringify(result, null, 2));

  await app.close();
}
main().catch((e) => {
  console.error(e);
  process.exit(1);
});
