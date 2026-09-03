# `gh-suggest` Specification

- Status: Implemented, pre-release
- Target: GitHub CLI extension with a bundled Agent Skill
- Contract version: 1

## Purpose

`gh-suggest` validates and optionally creates one pull-request review containing
1–100 ordered inline suggestions. It validates the complete group before one
non-idempotent POST, preventing partially published batches. The bundled Agent
Skill supports review-only dry runs and explicitly authorized posting.

## Sources of truth

This file owns architecture, safety invariants, implementation status, and
release work. Exact schemas and procedures live in:

| Concern | Artifact |
| --- | --- |
| Installation and user workflow | [`README.md`](README.md) |
| Review-file schema | [`schemas/review-file-v1.schema.json`](schemas/review-file-v1.schema.json) |
| Process-output schema | [`schemas/output-v1.schema.json`](schemas/output-v1.schema.json) |
| Reconciliation schema | [`schemas/reconciliation-descriptor-v1.schema.json`](schemas/reconciliation-descriptor-v1.schema.json) |
| Agent authorization and workflow | [`skills/gh-suggest/SKILL.md`](skills/gh-suggest/SKILL.md) |
| Agent-facing CLI contract | [`skills/gh-suggest/references/cli-contract.md`](skills/gh-suggest/references/cli-contract.md) |
| Version pins | [`.mise.toml`](.mise.toml), [`go.mod`](go.mod), [workflows](.github/workflows) |

All JSON contracts use `schemaVersion: 1`. Runtime validation owns rules JSON
Schema cannot express: duplicate keys, byte limits, path resolution, overlapping
ranges, and GitHub diff commentability.

## Scope

Version 1 supports deterministic human and agent workflows for single-line and
multi-line replacements on visible right-side diff lines. It provides write-free
dry runs, stable JSON output, an optional exact-head guard, one grouped `COMMENT`
review, and read-only reconciliation without exposing review prose or replacement
content in structured output or recovery descriptors.

It excludes left-side, file-level, and general comments; approval and
change-request workflows; pending-review, edit, delete, and thread management;
automatic write retries; GitHub Enterprise Server; interactive editing; and
automatic agent installation or configuration.

## Command contract

```text
gh suggest create [<number> | <url> | <branch>] --review-file PATH [flags]
gh suggest reconcile --attempt-file PATH [flags]
```

The optional create selector must precede flags. It accepts a positive pull
request number, a `github.com` pull-request URL, a branch, or `OWNER:BRANCH`.
Without a selector, the current branch must identify exactly one open pull
request. `--repo` accepts `OWNER/REPOSITORY` or `HOST/OWNER/REPOSITORY`; version 1
rejects hosts other than `github.com`.

### Create input

`--review-file` is required. Its strict UTF-8 JSON contains a nonblank review
body and ordered suggestions. Each suggestion has a clean repository-relative
path, positive inclusive end line, optional positive start line less than the end
line, replacement-file path, and optional Markdown note.

Ranges in one file cannot overlap. Relative replacement paths resolve from the
review file's directory. Standard input may supply the review file or one
replacement, never both or multiple replacements; `-` and common file-descriptor
zero aliases are the same source.

Normalization and limits:

- CRLF becomes LF in prose, notes, and replacements.
- Exactly one terminal replacement LF is removed; empty content means deletion.
- Review and ambiguous-attempt files are each limited to 1 MiB.
- Each normalized replacement and their aggregate are limited to 1 MiB.
- The normalized review body and all notes share a 64 KiB limit.
- Invalid UTF-8, NUL, response-unstable controls, and literal Unicode control
  escapes that go-gh rewrites on read-back are rejected.
- Notes cannot leave a fenced-code or explicitly terminated raw-HTML block open
  across the generated suggestion fence.

`--dry-run` performs every read and validation but omits the POST. `--head-sha`
requires both metadata observations to match the supplied full commit SHA.

### Reconcile input

`--attempt-file` is required and may be `-`. It consumes the complete
`write_outcome_unknown` JSON error envelope and its strict, non-sensitive
reconciliation descriptor. Additive outer-envelope fields are allowed;
descriptor fields are exact and closed.

## Safety invariants

### Validation order

Create follows one linear pipeline:

1. Parse flags and the bounded review file.
2. Validate fields available before replacement I/O.
3. Resolve local repository and host context without authentication when possible.
4. Read and normalize bounded replacements.
5. Authenticate and resolve exactly one pull request.
6. Read complete pull-request metadata.
7. Fetch and parse one bounded raw diff.
8. Prove every range is visible within one right-side text hunk.
9. Re-read metadata and reject base or head movement, including before returning
   a range failure that may have used a newer unpinned diff.
10. Render suggestion bodies and compute the ordered request digest.
11. Return a dry-run result or perform one review POST.

Preflight precedes caller-selected replacement-file access. Dry runs and writes
share preparation and stale-state checks. The second metadata read is best-effort
protection because GitHub provides neither an atomic expected-head condition nor
an idempotency key for review creation.

### Diff validation

A raw diff is accepted only when:

- it is below the 20 MiB truncation boundary and has a terminal LF;
- parsing succeeds without preamble or conflicting file metadata;
- parsed file count matches pull-request metadata;
- any Git file-header count is consistent;
- right-side paths are unique; and
- hunk ranges, counts, order, and coordinates are internally consistent and
  non-overlapping.

The retained index contains only file metadata and visible right-side hunk
ranges. Binary, deleted, mode-only, missing, renamed-old-path, invisible, and
cross-hunk targets fail closed with stable reasons.

### Review write

- The event is always `COMMENT`; suggestion order survives validation, hashing,
  transport, and output.
- The request digest binds repository, pull request, base and head SHAs, event,
  review-body digest, and each ordered path, range, side, and rendered-body digest.
- One POST submits the group. Its single-use body exposes no transport replay hook.
- A failure before the transport consumes the body is definitive; failure after
  transmission may begin is ambiguous.
- Success requires HTTP 200 and a positive review ID and URL in a bounded,
  decodable response.
- Post-transmission transport errors, HTTP 5xx, redirects, unexpected success
  statuses, and missing, oversized, or malformed responses produce
  `write_outcome_unknown` and are never retried automatically.
- If GitHub creation is definitive but local success output fails, return
  `io_error` with `posted: true`, review identity, and no-retry guidance.

### Reconciliation

Reconciliation uses only bounded paginated GETs. It considers canonicalized
`COMMENTED` reviews within two minutes on either side of the attempt, then
compares submitted commit SHA, review-body digest, complete comment count, and
the order-independent multiset of original coordinates and body digests. Current
coordinates are fallback data when originals are absent.

Exactly one complete match returns `likely_created` with its review ID and URL.
Zero, partial, or multiple matches remain `unknown`; neither status authorizes an
automatic retry.

### Transport

- Authentication comes from GitHub CLI after local input validation.
- GitHub CLI transport settings, including Unix-socket routing, resolve before
  installation of the write-redirect guard.
- JSON and raw-diff clients use their exact media types and the pinned API version.
- Requests are context-aware with a 30-second timeout; inherited API debugging is
  disabled to protect request bodies.
- GET and HEAD redirects are allowed; mutating-request redirects are rejected
  before replay.
- List endpoints share a bound of 100 pages with 100 results per page.

## Implementation

| Package | Responsibility |
| --- | --- |
| `attempt` | Ambiguous-write descriptor and strict decoding |
| `cli` | Arguments, bounded local input, wiring, and process mapping |
| `create` | Prepare, validate, digest, and optional post use case |
| `diff` | Complete diff parsing and right-side visibility |
| `domain` | Shared target, state, event, failure, and validation vocabulary |
| `github` | Authenticated endpoints and transport policy |
| `jsoninput` | Duplicate detection, exact field names, and single-value enforcement |
| `output` | Versioned JSON and human output |
| `reconcile` | Read-only whole-review matching |
| `resolve` | Repository, selector, branch, and Git resolution |
| `suggestion` | Content normalization, Markdown rendering, and digests |
| `schemas` | Draft 2020-12 contracts and conformance tests |

Core use cases depend on narrow consumer-owned interfaces. Transport, Git,
environment, signals, files, and process streams stay at application boundaries.

Direct dependencies are `go-gh/v2` for GitHub CLI authentication and REST
transport, `go-gitdiff` for syntax parsing under local completeness policy, and
test-only `jsonschema/v6` for schema conformance. The standard library handles
CLI parsing, subprocesses, hashing, JSON encoding, and orchestration.

## Validation

Tests cover strict input; normalization and limits; selector resolution; complete
diff parsing; grouped request ordering and stale-state rejection; transport
headers, pagination, bounds, replay prevention, redirects, and ambiguous errors;
dry-run/no-write behavior; whole-review reconciliation; output and schema
contracts; and local I/O failures.

The canonical non-mutating gate is:

```sh
mise run check
```

It checks formatting, module metadata, lint, race-enabled tests, the bundled
skill, and whitespace. CI runs the same gate without modifying the checkout and
separately tests local skill installation across supported agents, scopes, and
the workflow's minimum/current GitHub CLI versions (2.90.0 and pinned 2.99.0).

## Release

The tag workflow must:

1. Validate the semantic version and source tree.
2. Validate skill publication without publishing.
3. Create or safely reuse a correctly classified draft release.
4. Build and attest precompiled assets.
5. Run each draft platform asset without credentials.
6. Preview and install the tagged skill for every supported agent.
7. Publish only after all draft smoke tests pass.
8. Install and run the published extension on every supported platform.

Prerelease classification ignores SemVer build metadata. Published releases are
never replaced. A prerelease requires an earlier published precompiled release
so GitHub CLI recognizes the repository as a binary extension.

### Remaining pre-release work

- Run an authorized live smoke test in a dedicated repository.
- Confirm one grouped review contains ordered, applicable single-line and
  multi-line suggestions.
- Confirm empty deletion and variable-length backtick replacements apply exactly.
- Confirm the returned URL and ID identify the grouped review.
- Run the tagged asset and skill installation matrix through the release workflow.

Live mutation tests must not target unrelated repositories or pull requests. The
project remains pre-release until this work succeeds.
