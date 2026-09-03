# gh-suggest CLI contract

## Contents

- [Create command](#create-command)
- [Review-file contract](#review-file-contract)
- [Stable success output](#stable-success-output)
- [Stable failures](#stable-failures)
- [Ambiguous writes](#ambiguous-writes)
- [Compatibility](#compatibility)

## Create command

```text
gh suggest create [<number> | <url> | <branch>] --review-file PATH
```

The optional selector must precede every flag. A positive integer selects a
pull request; a full `https://github.com/OWNER/REPO/pull/NUMBER` URL selects its
repository and pull request; `BRANCH` or `OWNER:BRANCH` must resolve to exactly
one open pull request. With no selector, the current branch must resolve
uniquely.

Only `github.com` and right-side diff lines are supported. Every invocation
performs authenticated reads, validates every target in one complete diff,
confirms base and head metadata twice, renders every body, and returns one
request digest. By default, `create` posts after validation; `--dry-run` stops
before the POST. Optional `--head-sha` requires both metadata reads to match a
caller-known snapshot.

The CLI has no separate single-suggestion mode. A one-item `suggestions` array
creates one review containing one inline suggestion; an array with 2–100 items
creates one review containing the complete ordered group. One authorization
decision covers the exact array, so changing its membership or order requires
fresh authorization before posting.

## Review-file contract

The review file is UTF-8 JSON with `schemaVersion: 1`, one nonblank
`reviewBody`, and 1–100 ordered suggestions. Unknown fields, duplicate keys,
trailing JSON values, overlapping same-file ranges, and missing replacement
files are invalid.

The repository publishes the normative Draft 2020-12 structures in
`schemas/review-file-v1.schema.json`, `schemas/output-v1.schema.json`, and
`schemas/reconciliation-descriptor-v1.schema.json`. Runtime validation also
enforces semantic, file-system, and GitHub diff rules outside JSON Schema.

Each suggestion requires `path`, positive `endLine`, and `replacementFile`.
Use positive `startLine < endLine` for a multi-line range. `note` is optional
Markdown prose and must not leave a fence open.

Relative replacement paths resolve from the review file's directory. A review
file may come from stdin with `--review-file -`; alternatively, at most one
replacement may use `replacementFile: "-"`. The review file and a replacement
cannot both consume stdin. File-descriptor-zero aliases such as `/dev/stdin`,
`/dev/fd/0`, and `/proc/self/fd/0` have the same ownership rules as `-`.

CRLF becomes LF in prose and replacements. One terminal replacement LF is
removed. Empty replacement content means deletion. Limits are:

- 1 MiB raw review file;
- 1 MiB per normalized replacement;
- 1 MiB aggregate normalized replacement content;
- 64 KiB aggregate normalized review body and suggestion notes.

Review prose, notes, and replacements cannot contain control characters or
literal Unicode control escapes that GitHub response reads would alter. A note
also cannot leave a fenced code block or a raw HTML block requiring an explicit
terminator open across the generated suggestion fence.

The request digest binds repository, pull request, base and head SHAs, fixed
event `COMMENT`, the review-body digest, and the ordered list of every
path/range/side/rendered-body digest.

## Stable success output

With `--json`, stdout is exactly one schema-version 1 object followed by LF.
Both statuses include target metadata, `reviewBodySHA256`, ordered
`suggestions` with ranges and replacement/body hashes, and `requestSHA256`.

- Dry-run: `status: "validated"` and `posted: false`.
- Created: `status: "created"`, `posted: true`, positive `reviewID`, and `url`.

Plaintext review prose, replacement content, and authentication material are
never echoed.

## Stable failures

With `--json`, stderr is exactly one schema-version 1 error envelope followed
by LF and stdout is empty.

| Code | Exit | Action |
| --- | ---: | --- |
| `invalid_arguments` | 2 | Correct local flags, manifest, or content. |
| `authentication_failed` | 3 | Authenticate GitHub CLI. |
| `target_not_found` | 4 | Recheck repository and pull request. |
| `range_not_commentable` | 5 | Refresh and select verified diff ranges. |
| `stale_pull_request` | 6 | Refresh the review against the current snapshot. |
| `github_api_error` | 7 | Inspect the definitive GitHub rejection. |
| `io_error` | 8 | Fix local I/O. If `posted: true`, report the review and never retry. |
| `rate_limited` | 9 | Respect retry metadata; do not auto-retry. |
| `write_outcome_unknown` | 10 | Reconcile read-only; never retry blindly. |
| `unsupported_host` | 11 | Use `github.com`. |
| `cancelled` | 12 | Retry only read-only work when appropriate. |
| `permission_denied` | 13 | Obtain pull-request write permission. |

Consumers must ignore additive unknown fields while `schemaVersion` remains 1.

## Ambiguous writes

GitHub provides no idempotency key or atomic expected-head condition for review
creation. A transport failure, HTTP 5xx response, redirect, or undecodable
response after the POST begins may mean the whole or a partial review exists.

`write_outcome_unknown` is always emitted as a complete JSON error envelope,
even if the write omitted `--json`. Save that envelope and run:

```sh
gh suggest reconcile --attempt-file <error.json> --json
```

The embedded schema-version 1 reconciliation descriptor contains only
repository/PR coordinates, base/head SHAs, event, attempt time, and review,
request, and per-suggestion body digests. It contains no review prose or
replacement content.

Reconciliation uses only bounded paginated GET requests. It filters submitted
`COMMENTED` reviews within two minutes of the attempt, compares commit and
review-body digest, reads each candidate review's comments, and compares the
complete order-independent multiset of original target coordinates and body
digests.

One complete match returns `likely_created` with `reviewID` and URL. Zero,
partial, or multiple complete matches return `unknown`. No result authorizes an
automatic retry.

## Compatibility

Install the extension and skill from the same tag. Skill discovery requires
GitHub CLI 2.90.0 or newer while `gh skill` remains preview. Supported agent
identifiers are `claude-code`, `cursor`, and `codex`.

Use `gh --version` and `gh suggest --help` to check runtime availability. Help
output does not prove extension/skill tag parity. If the extension is absent,
use `gh extension install pvzig/gh-suggest --pin <TAG>` only after obtaining the
matching tag.
