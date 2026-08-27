import { Logger } from '@nestjs/common';
import { ConfigService } from '@nestjs/config';
import * as fs from 'node:fs';
import * as os from 'node:os';
import * as path from 'node:path';
import { BaseGrpcClient } from './base-grpc-client';

// Minimal concrete subclass -- onModuleInit needs a real proto file to
// load (protoFile/protoServicePath), so this targets the same
// orchestrator.proto every real client already uses. The mTLS logic
// under test (buildChannelCredentials) runs before any RPC is made, so
// no live orchestrator connection is needed for these assertions --
// grpc-js's client constructor doesn't dial eagerly.
class TestGrpcClient extends BaseGrpcClient {
  protected readonly logger = new Logger('TestGrpcClient');
  protected readonly protoFile = 'orchestrator.proto';
  protected readonly protoServicePath =
    'practiceengine.orchestrator.v1.EnvironmentOrchestrator';
  protected connectionLogMessage(address: string): string {
    return `test client connected to ${address}`;
  }
}

function config(values: Record<string, string | undefined>): ConfigService {
  return { get: (key: string) => values[key] } as unknown as ConfigService;
}

describe('BaseGrpcClient mTLS credential construction', () => {
  let tmpDir: string;

  beforeEach(() => {
    tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), 'mtls-test-'));
  });

  afterEach(() => {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  });

  function writeFile(name: string, content = 'fake-pem-content'): string {
    const p = path.join(tmpDir, name);
    fs.writeFileSync(p, content);
    return p;
  }

  it('uses insecure credentials (unchanged default) when ORCHESTRATOR_TLS_CA is not set', () => {
    const client = new TestGrpcClient(config({}));
    expect(() => client.onModuleInit()).not.toThrow();
  });

  it('throws at module-init time if ORCHESTRATOR_TLS_CA is set but client cert/key are missing (never falls back to plaintext)', () => {
    const caPath = writeFile('ca.crt');
    const client = new TestGrpcClient(config({ ORCHESTRATOR_TLS_CA: caPath }));
    expect(() => client.onModuleInit()).toThrow(/ORCHESTRATOR_CLIENT_TLS_CERT/);
  });

  it('throws a clear error if a configured cert file cannot be read (never falls back to plaintext)', () => {
    const client = new TestGrpcClient(
      config({
        ORCHESTRATOR_TLS_CA: '/nonexistent/ca.crt',
        ORCHESTRATOR_CLIENT_TLS_CERT: '/nonexistent/client.crt',
        ORCHESTRATOR_CLIENT_TLS_KEY: '/nonexistent/client.key',
      }),
    );
    expect(() => client.onModuleInit()).toThrow(/could not be read/);
  });

  it('succeeds in building SSL credentials when all three files are present and contain valid PEM data', () => {
    // grpc-js's createSsl parses the PEM content immediately, so the
    // fixtures need to be real certs/keys, not arbitrary bytes -- reuse
    // this repo's own dev CA (scripts/gen-certs.sh output) rather than
    // hand-rolling a throwaway keypair in the test.
    const certsDir = path.resolve(__dirname, '..', '..', '..', 'certs');
    const caPath = path.join(certsDir, 'ca.crt');
    const certPath = path.join(certsDir, 'practice-core-client.crt');
    const keyPath = path.join(certsDir, 'practice-core-client.key');
    if (!fs.existsSync(caPath)) {
      console.warn(
        'skipping: certs/ not generated (run scripts/gen-certs.sh first)',
      );
      return;
    }

    const client = new TestGrpcClient(
      config({
        ORCHESTRATOR_TLS_CA: caPath,
        ORCHESTRATOR_CLIENT_TLS_CERT: certPath,
        ORCHESTRATOR_CLIENT_TLS_KEY: keyPath,
      }),
    );
    expect(() => client.onModuleInit()).not.toThrow();
  });
});
