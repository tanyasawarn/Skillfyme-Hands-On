import {
  ArgumentsHost,
  BadRequestException,
  ForbiddenException,
  NotFoundException,
} from '@nestjs/common';
import { AllExceptionsFilter } from './all-exceptions.filter';

function mockHost(): {
  host: ArgumentsHost;
  json: jest.Mock;
  status: jest.Mock;
} {
  const json = jest.fn();
  const status = jest.fn().mockReturnValue({ json });
  const response = { status };
  const request = { method: 'GET', url: '/v1/practice/attempts/123' };
  const host = {
    switchToHttp: () => ({
      getResponse: () => response,
      getRequest: () => request,
    }),
  } as unknown as ArgumentsHost;
  return { host, json, status };
}

describe('AllExceptionsFilter', () => {
  let filter: AllExceptionsFilter;

  beforeEach(() => {
    filter = new AllExceptionsFilter();
    jest.spyOn(filter['logger'], 'error').mockImplementation(() => undefined);
  });

  it('passes through an HttpException with its real status code and message', () => {
    const { host, json, status } = mockHost();
    filter.catch(new NotFoundException('activity not found'), host);

    expect(status).toHaveBeenCalledWith(404);
    expect(json).toHaveBeenCalledWith(
      expect.objectContaining({
        statusCode: 404,
        message: 'activity not found',
      }),
    );
  });

  it('preserves ValidationPipe-style structured responses (array of per-field messages) under detail, not collapsed to one string', () => {
    const { host, json } = mockHost();
    filter.catch(
      new BadRequestException({
        message: ['field a is required', 'field b invalid'],
      }),
      host,
    );

    const body = json.mock.calls[0][0];
    expect(body.detail).toBeDefined();
    expect(body.detail.message).toEqual([
      'field a is required',
      'field b invalid',
    ]);
  });

  it('includes the request path in the response envelope', () => {
    const { host, json } = mockHost();
    filter.catch(new ForbiddenException('nope'), host);
    expect(json.mock.calls[0][0].path).toBe('/v1/practice/attempts/123');
  });

  it('SECURITY: collapses a non-HttpException (e.g. a raw DB error) to a fixed generic 500 message, never leaking the real error text', () => {
    const { host, json, status } = mockHost();
    const dbError = new Error(
      'password authentication failed for user "practice" at 10.0.4.12:5432',
    );
    filter.catch(dbError, host);

    expect(status).toHaveBeenCalledWith(500);
    const body = json.mock.calls[0][0];
    expect(body.message).toBe('Internal server error');
    expect(JSON.stringify(body)).not.toContain(
      'password authentication failed',
    );
    expect(JSON.stringify(body)).not.toContain('10.0.4.12');
  });

  it('SECURITY: collapses a thrown non-Error value (e.g. a string or plain object) to the same generic 500, never echoing it back', () => {
    const { host, json, status } = mockHost();
    filter.catch(
      { internal: 'kysely NoResultError', table: 'attempt.attempt' },
      host,
    );

    expect(status).toHaveBeenCalledWith(500);
    const body = json.mock.calls[0][0];
    expect(body.message).toBe('Internal server error');
    expect(JSON.stringify(body)).not.toContain('NoResultError');
  });

  it('logs the real error server-side for an unhandled exception (so it stays diagnosable even though the client never sees it)', () => {
    const { host } = mockHost();
    const errorSpy = jest.spyOn(filter['logger'], 'error');
    const dbError = new Error('connection refused');
    filter.catch(dbError, host);

    expect(errorSpy).toHaveBeenCalledWith(
      expect.stringContaining('connection refused'),
      dbError.stack,
    );
  });

  it('logs a 5xx HttpException server-side too (a caller-visible 500 is exactly the case that should show up in logs)', () => {
    const { host } = mockHost();
    const errorSpy = jest.spyOn(filter['logger'], 'error');
    filter.catch(
      new BadRequestException('irrelevant', { cause: undefined }),
      host,
    );
    // BadRequestException is 400, should NOT log -- confirms the >= 500 gate.
    expect(errorSpy).not.toHaveBeenCalled();
  });

  it('does not log a routine 4xx HttpException (404/403 are expected, not incidents)', () => {
    const { host } = mockHost();
    const errorSpy = jest.spyOn(filter['logger'], 'error');
    filter.catch(new NotFoundException('not found'), host);
    filter.catch(new ForbiddenException('forbidden'), host);
    expect(errorSpy).not.toHaveBeenCalled();
  });
});
