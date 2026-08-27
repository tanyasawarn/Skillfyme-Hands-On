# `src/common/`

Shared code that more than one module under `src/modules/` needs. Put shared things here, not
in whichever module happened to need them first.

## The rule

If a piece of logic — a constant, a type, a base class, a helper function, an exception filter —
is genuinely needed by two or more modules, it belongs in `common/`, not duplicated into each
module or bolted onto whichever module wrote it first. This directory existed, empty, before any
of the code in it was written; it was scaffolded specifically as the answer to "where does shared
code go," because an earlier audit of this codebase found 119 duplicated/hardcoded patterns
across the project — a direct consequence of there being no designated place for shared code, so
each contributor re-solved (and re-drifted) the same problem independently. Two of those
duplications had already caused live, shipped bugs (a status rendering inconsistently across two
pages; a quota's enforced value silently disagreeing with its own error message) by the time the
audit ran.

This directory is organized by **what a thing *is*** (a constants module, a type, a repository
base, a grpc base, a nats base, an error helper, an exception filter), not by which module
first needed it — that's what keeps a future contributor able to find "is there already a
`findOrThrow`-style helper for this?" without having to know which module happened to invent it.

## What's here today

| File | What it is |
|---|---|
| `activity-spec-reader.ts` | `ActivitySpecReader` — the `attempt → activity_version → spec_jsonb` join, shared by every module that just needs the parsed spec for an attempt (not the wider attempt/version row lookups `attempt.service.ts`/`evaluation.service.ts` do themselves) |
| `all-exceptions.filter.ts` | `AllExceptionsFilter` — the global NestJS exception filter, ensures a non-`HttpException` error never leaks raw DB/internal error detail to a client |
| `attempt-status-groups.ts` | `AttemptStatusGroups` — the one place `AttemptStatus` values are grouped into TERMINAL / RETRYABLE_FROM / SUCCESS, derived from the schema's own union rather than hand-copied per call site |
| `base-grpc-client.ts` | `BaseGrpcClient` — proto-loading, address resolution, and the promisified `call()` helper shared by both real gRPC clients (orchestrator, validator executor) |
| `base.repository.ts` | `BaseRepository` — the shared `@Inject(KYSELY)` constructor every pure repository class extends |
| `constants.ts` | `MasteryConstants`, `GrpcClientConstants`, `TimeoutConstants`, `EligibilityConstants`, `CacheSweepConstants` — magic numbers that were independently duplicated at 2+ real call sites, one named export per genuinely-shared concept (a coincidentally-matching number used for an unrelated concept elsewhere is deliberately NOT folded in here) |
| `find-or-throw.ts` | `findOrThrow(row, message)` — the "look up a row, throw `NotFoundException` if missing" pattern |
| `nats-subscriber-base.ts` | `NatsSubscriberBase` — connect/subscribe/drain/close lifecycle shared by both real NATS consumers |
| `published-activity-versions-query.ts` | `publishedActivityVersionsQuery()` — the composable Kysely query fragment for the tenant-scoped, PUBLISHED-status `activity_version`/`activity` join |
| `repo-relative-path.ts` | `resolveRepoRelativePath()` — the `__dirname`-then-`process.cwd()`-fallback path resolution every file reading something from `contracts/`/`content/` needs |

Each non-trivial file has a matching `.spec.ts` in this same directory.

## A structural note, for anyone expecting subdirectories

An earlier version of the plan for this directory proposed splitting it into
`{constants,types,repository,grpc,nats,errors,guards}/` subdirectories. In practice the directory
stayed flat — ten files is small enough that a flat listing is easier to scan than seven
near-empty subdirectories, and every file name already states what it is. If this directory grows
substantially past its current size, revisit that split; don't add it preemptively.

## Where security-sensitive guards live

`AttemptOwnershipGuard` and the `Role` enum's `isRole()` runtime check are deliberately **not**
in `common/` — both live next to the module that owns the security boundary they enforce
(`modules/attempt/attempt-ownership.guard.ts`, `modules/auth/role.ts`) and are kept dependency-
free on purpose (this project's Jest config can't load any file that transitively imports
`kysely`'s ESM build, so a security-critical pure-logic check needs to stay import-light to be
directly unit-testable). If you're adding another auth/ownership check, follow that same
pattern — colocated with its module, not folded into `common/`.
