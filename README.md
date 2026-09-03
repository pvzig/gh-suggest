<p align="center"><img width="35%" height="35%" alt="gh-suggest" src="https://github.com/user-attachments/assets/4fa610b7-3fd2-44e6-8b6c-ec0ac284eca2" /></p>

# gh-suggest

`gh-suggest` is a precompiled GitHub CLI extension that lets humans and coding
agents validate and submit one pull-request review containing one or more exact
inline suggestions. A review may mix single-line and multi-line replacements.

## Install

Install the extension and bundled Agent Skill from the same release:

```sh
gh extension install pvzig/gh-suggest --pin v0.1.0

gh skill preview pvzig/gh-suggest gh-suggest@v0.1.0
gh skill install pvzig/gh-suggest gh-suggest \
  --pin v0.1.0 \
  --agent codex \
  --scope user
```

Replace `codex` with `claude-code` or `cursor` when appropriate. Use
`--scope project` for a project-local skill. `gh skill` is a preview GitHub CLI
feature that requires GitHub CLI 2.90.0 or newer.

## Create a grouped review

Describe the complete review in one JSON file. Each `replacementFile` contains
only the replacement code:

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

Omit `startLine` for a one-line suggestion. Multi-line ranges are inclusive.
Suggestions must be ordered, non-overlapping, and target visible right-side
lines in the same pull request.

### Validate without posting

Start with a dry run:

```sh
gh suggest create 123 \
  --repo OWNER/REPOSITORY \
  --review-file review/review.json \
  --dry-run \
  --json
```

A dry run performs the same GitHub reads and validation as a write but omits the
POST. Its output includes `status: "validated"`, `posted: false`, the resolved
base and head SHAs, suggestion digests, and `requestSHA256`.

### Post the review

Posting is one external write that can notify people. After explicitly deciding
to post the exact validated review, rerun without `--dry-run`. Use the head SHA
from the dry run to reject a changed pull-request snapshot:

```sh
gh suggest create 123 \
  --repo OWNER/REPOSITORY \
  --review-file review/review.json \
  --head-sha <HEAD_SHA> \
  --json
```

A successful write submits one `COMMENT` review containing every ordered
suggestion. It does not approve the pull request or request changes.

### Input behavior

- Relative replacement paths resolve from the review file's directory. When the
  review file is read from standard input, they resolve from the working directory.
- An empty replacement deletes the selected range. One replacement may use `-`
  for standard input, but the review file and multiple replacements cannot share it.
- The selector may be a pull-request number, full `github.com` pull-request URL,
  branch, or `OWNER:BRANCH`. With no selector, the current branch must resolve to
  exactly one open pull request.
- A review contains 1–100 suggestions. Review files, replacement content, prose,
  and reconciliation input are bounded and strictly validated.

The versioned contracts are:

- [`schemas/review-file-v1.schema.json`](schemas/review-file-v1.schema.json)
- [`schemas/output-v1.schema.json`](schemas/output-v1.schema.json)
- [`schemas/reconciliation-descriptor-v1.schema.json`](schemas/reconciliation-descriptor-v1.schema.json)
- [`skills/gh-suggest/references/cli-contract.md`](skills/gh-suggest/references/cli-contract.md)

## Reconcile an ambiguous write

Preserve standard error from a write attempt. If it contains
`write_outcome_unknown`, the POST may have succeeded: do not retry it.

```sh
gh suggest reconcile \
  --attempt-file create-error.json \
  --json
```

Reconciliation performs only GET requests. One complete match returns
`status: "likely_created"` with the review ID and URL. Any other result remains
`status: "unknown"` and does not make a retry safe.

## Safety guarantees

- Every suggestion range is checked against the complete pull-request diff.
- Pull-request metadata is read before and after diff validation; a changed base
  or head prevents the write.
- One request submits the complete review through one typed POST boundary.
- A possibly transmitted POST is never retried automatically and can be
  reconciled read-only.
- Structured output excludes tokens, review prose, and replacement content.

See [SPEC.md](SPEC.md) for architecture, limits, validation order, and release
requirements. The bundled [Agent Skill](skills/gh-suggest/SKILL.md) defines the
authorization boundary for agent-driven reviews.

## Local development

The project uses [mise](https://mise.jdx.dev/) for its pinned toolchain.

```sh
mise install
go build -o gh-suggest ./cmd/gh-suggest
gh extension install .

gh skill install . gh-suggest \
  --from-local \
  --agent codex \
  --scope project
```

Run formatting and the complete validation gate:

```sh
mise run format
mise run check
```

Live mutation smoke tests belong only in a dedicated test repository and pull
request.
