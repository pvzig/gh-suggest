# gh-suggest

`gh-suggest` is a precompiled GitHub CLI extension for creating PR suggestions. It supports
single-line and multi-line replacements in the same grouped review. It is
designed for both humans and coding agents.

## Install

Pin the extension and bundled Agent Skill to the same release:

```sh
gh extension install pvzig/gh-suggest --pin <TAG>

gh skill preview pvzig/gh-suggest gh-suggest@<TAG>
gh skill install pvzig/gh-suggest gh-suggest \
  --pin <TAG> \
  --agent codex \
  --scope user
```

Replace `codex` with `claude-code` or `cursor` when appropriate. Use
`--scope project` for a project-local skill installation. `gh skill` is a
preview GitHub CLI feature and requires GitHub CLI 2.90.0 or newer.

## Create a grouped review

Describe the review in a JSON file. Each `replacementFile`
contains only replacement code; a relative path is resolved from the directory
containing the review file, not from the process working directory.

The machine-readable contracts use JSON Schema Draft 2020-12:

- [`schemas/review-file-v1.schema.json`](schemas/review-file-v1.schema.json)
  defines review-file input.
- [`schemas/output-v1.schema.json`](schemas/output-v1.schema.json) defines
  create, reconciliation, and error output.
- [`schemas/reconciliation-descriptor-v1.schema.json`](schemas/reconciliation-descriptor-v1.schema.json)
  defines the recovery descriptor nested in an ambiguous-write error.

For example, `review/review.json` can reference two relative replacement files:

```json
{
  "schemaVersion": 1,
  "reviewBody": "Two focused concurrency fixes.",
  "suggestions": [
    {
      "path": "Sources/Example.swift",
      "endLine": 42,
      "replacementFile": "replacements/cancellation.swift",
      "note": "Preserve cancellation propagation."
    },
    {
      "path": "Sources/Worker.swift",
      "startLine": 64,
      "endLine": 66,
      "replacementFile": "replacements/timeout.swift",
      "note": "Keep timeout handling structured."
    }
  ]
}
```

The first entry is a one-line suggestion, so it omits `startLine`; the second
uses an inclusive multi-line range. Suggestion order is preserved. Create the
review in one command:

```sh
gh suggest create 123 \
  --repo OWNER/REPOSITORY \
  --review-file review/review.json \
  --json
```

A successful write validates the complete diff and every range, re-reads the
pull-request metadata, then submits exactly one `COMMENT` review containing all
ordered suggestions. It does not approve the pull request or request changes.

Use `--dry-run` to perform the same validation without writing. A dry run returns `status: "validated"`, `posted: false`, the resolved base and
head SHAs, the review-body digest, an ordered summary of every suggestion and
replacement digest, and `requestSHA256`.

Use an empty replacement file to delete its selected range. One suggestion may
use `"replacementFile": "-"` to read replacement text from standard input, but
standard input cannot supply both the review file and a replacement, or several
replacements. When `--review-file -` is used, other relative replacement paths
are resolved from the current working directory. Common `/dev` and `/proc`
aliases for file descriptor zero are treated as standard input too.

The review body must be nonempty. Suggestion notes must not end inside a
Markdown code fence; an unclosed fence would absorb the suggestion block, so it
is rejected. `--head-sha` is an optional full 40-character expected-head guard
for callers that already know which pull-request snapshot they reviewed. It is
accepted for direct writes and dry runs.

The selector may be a positive pull-request number, a full GitHub pull-request
URL, `BRANCH`, or `OWNER:BRANCH`. With no selector, the current Git branch must
resolve to exactly one open pull request.

### Choose one or several suggestions

There is one workflow rather than separate single and batch commands. Use a
one-item `suggestions` array when exactly one actionable change is ready. Group
multiple ready, non-overlapping changes when they belong to the same pull
request snapshot and should be reviewed or posted together; this minimizes
external writes and notifications.

Split findings only when they need separate timing, review context, or
authorization, or when a count or size limit requires it. Every split is a
separate write and therefore needs explicit posting authorization. Never
silently add suggestions to an authorized review or split one authorized
review into multiple writes. Do not split overlapping
alternatives merely to bypass manifest validation; choose one alternative or
ask which one should be prepared.

### Input limits

- A review contains between 1 and 100 suggestions, all targeting right-side
  lines in the same pull request.
- The UTF-8 JSON review file is limited to 1 MiB. Field names are
  case-sensitive and must match the schema exactly; unknown and duplicate JSON
  fields are rejected.
- Each normalized replacement is limited to 1 MiB, and all normalized
  replacements in the review are also limited to 1 MiB in aggregate.
- The normalized review body and all suggestion notes are limited to 64 KiB in
  aggregate.
- Review prose, notes, and replacements reject control characters—and literal
  Unicode control escapes—that go-gh cannot read back byte-for-byte for
  reconciliation. Notes also reject unclosed fenced code blocks or raw HTML
  blocks requiring an explicit terminator when they would consume the following
  suggestion fence.
- An ambiguous-attempt file used for reconciliation is limited to 1 MiB.

## Reconcile an ambiguous write

JSON errors are written to standard error. A `write_outcome_unknown` error is
always emitted as JSON, even when `--json` was omitted, so its recovery
descriptor remains machine-readable. Preserve the original write's
error envelope—for example, append `2>create-error.json` to that write command.
If its error code is `write_outcome_unknown`, the POST may have succeeded: do
not repeat it. Pass that exact saved JSON error to the read-only reconciliation
command:

```sh
gh suggest reconcile \
  --attempt-file create-error.json \
  --json
```

Use `--attempt-file -` to read the saved envelope from standard input. One exact
whole-review match returns schema version 1 with `status: "likely_created"`,
`reviewID`, and `url`. Zero or multiple exact matches—and partial comment-set
matches—return `status: "unknown"` with match counts. The write remains
unresolved and must not be retried.

Reconciliation performs only GET requests. It compares the review body,
submitted commit, complete suggestion-comment set, and submission time within
two minutes on either side of the recorded attempt.

## Safety model

- Every dry run and write authenticates, reads pull-request metadata twice,
  retrieves and validates the complete diff, validates every requested range,
  and constructs the exact grouped-review request before deciding whether to
  POST.
- Direct posting needs no prior validation receipt. An optional `--head-sha`
  fails closed if the pull request is not on the expected snapshot.
- A base or head change between the two metadata reads prevents the write.
- Creation has one typed mutation boundary: one POST submits one `COMMENT`
  review containing the complete ordered suggestion list.
- The final metadata check is best-effort because GitHub offers no atomic
  expected-head condition or idempotency key for review creation.
- A possibly transmitted POST is reported as `write_outcome_unknown` and is
  never retried automatically; `gh suggest reconcile` is read-only.
- If GitHub confirms creation but local success output fails, `io_error` details
  include `posted: true`, the review ID and URL, and no-retry guidance.
- JSON output never includes tokens or full replacement content, and inherited
  `GH_DEBUG` output is disabled.

See [SPEC.md](SPEC.md) for the complete CLI, API, output, and release contract.

## Local development

This project uses [mise](https://mise.jdx.dev/) for its pinned toolchain.

```sh
mise install
go build -o gh-suggest ./cmd/gh-suggest
gh extension install .

gh skill install . gh-suggest \
  --from-local \
  --agent codex \
  --scope project
```

Run the local formatting and validation gate:

```sh
mise run format
mise run check
```

`mise run check` is read-only. It verifies formatting and module metadata, runs
the linter and race-enabled test suite, validates the bundled skill, and checks
staged and unstaged diffs for whitespace errors.

Live mutation smoke tests belong only in a dedicated test repository and pull
request.
