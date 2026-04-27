# T-017 — Top-level README + Makefile

> Up: [Tasks index](./README.md)

| Field | Value |
|---|---|
| Status | Not started |
| Priority | P1 |
| Estimate | 0.5 h (±25%) |
| Owner | unassigned |
| Parallel batch | B6 |
| Blocks | none |
| Blocked by | T-015, T-016 |
| Touches files | `README.md`, `Makefile` |

## Goal

Write the project root `README.md` (the one a reviewer sees first when they clone the repo) and a thin `Makefile` for `make test` and `make run`. Specified in [`ARCHITECT_PLAN.md` §5 phase 6](../system_design/ARCHITECT_PLAN.md#phase-6--readme--repo-polish-10-h) and the self-check in [§9](../system_design/ARCHITECT_PLAN.md#9-self-check-before-declaring-done).

## Context

The reviewer's first 5 minutes determine the impression. The README must answer four questions, briefly, in order:

1. What does this build? (one-paragraph)
2. How do I run it? (one shell block)
3. How do I test it? (one shell block)
4. Where is the design? (link to `docs/system_design/README.md`)

Plus a short "Decisions" section that mirrors the [decision summary table](../system_design/README.md#decision-summary) so the reviewer can scan the load-bearing choices without leaving the README.

The README is **not** a tutorial. It does not explain matching engines or stop orders. Anyone doing review of this codebase already knows what those are.

## Acceptance criteria

- [ ] `README.md` (project root, NOT `docs/system_design/README.md`) exists
- [ ] First section: one paragraph "what this is" (in-memory single-pair matching engine, BTC/IDR, four order types, four HTTP endpoints).
- [ ] "Run" section: `go run ./cmd/server` (or `make run`); shows a `curl` example for `POST /orders` matching the brief's example payload
- [ ] "Test" section: `go test ./...` and `go test ./... -race -count=10`; calls out the determinism replay 1000× run if it's not the default
- [ ] "Layout" section: a tree pointing to `internal/domain`, `internal/engine`, `internal/adapters`, `internal/app`, `cmd/server` with one-line description each, and a pointer to [`docs/system_design/README.md`](./docs/system_design/README.md) for the full design
- [ ] "Decisions" section: 6–10 bullet points mirroring the [`docs/system_design/README.md` decision table](./docs/system_design/README.md#decision-summary). Each bullet is one sentence, no rationale (rationale lives in the design docs)
- [ ] "What's not in v1" section: a bullet list mirroring [`ARCHITECT_PLAN.md` §2](../system_design/ARCHITECT_PLAN.md#2-constraints-and-explicit-non-goals) verbatim or near-verbatim. This is what protects against "but did you build X?" probes
- [ ] `Makefile` with three targets:
    - `make test` → `go test ./...`
    - `make race` → `go test ./... -race -count=10`
    - `make run` → `go run ./cmd/server`
    - (Optional) `make replay` → `go test ./internal/engine/ -run TestDeterministicReplay -count=1000`
- [ ] README links to `docs/challenges/trading-engine.pdf` (the brief) so a reviewer can pull it up alongside
- [ ] README is under one screen of vertical scroll (target: < 200 lines including code blocks). Anything more goes into `docs/system_design/`
- [ ] No trademarked / branded language ("blazing fast", "production-ready", etc.); engineering tone matches the design docs

## Implementation notes

- Use the exact `curl` example from [§08 Example payloads](../system_design/08-http-api.md#example-payloads) — copy-paste, do not paraphrase. The wire format is normative.
- The README's "Decisions" section is for skim-readability; it cannot replace the design docs and shouldn't try.
- Do not duplicate the full file tree from [§01](../system_design/01-architecture.md). Show top-level dirs only, link out for the full tree.
- If the curl example fails when manually run, fix the curl command (or the handler), not the design doc — the design is locked.

## Out of scope

- `docker-compose.yml` (optional per [`ARCHITECT_PLAN.md` §5 phase 6](../system_design/ARCHITECT_PLAN.md#phase-6--readme--repo-polish-10-h); skip unless specifically requested).
- API documentation (OpenAPI / Swagger) — out of v1.
- Architecture diagrams in the project README — those live in the design docs and are linked.
- Setup instructions for IDEs.

## Tests required

None directly. The Makefile targets run existing test suites. The README is verified by manual smoke:

- Reviewer clones, `make run`, `make test` — both work.
- Reviewer reads README in < 5 minutes and can answer "what does this do, how do I run it, where's the design."

## Definition of done

- [ ] All acceptance criteria checked
- [ ] `make test` and `make run` work from a clean clone
- [ ] curl example in the README round-trips against a running server
- [ ] README under 200 lines
- [ ] Cross-link from `README.md` → `docs/system_design/README.md` and vice-versa
