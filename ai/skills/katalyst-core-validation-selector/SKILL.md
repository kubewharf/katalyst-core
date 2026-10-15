---
name: "katalyst-core-validation-selector"
description: "Selects the right validation steps for touched katalyst-core paths, especially when make test coverage is non-obvious."
---

# Katalyst Core Validation Selector

Use this skill to choose the smallest correct validation set for a `katalyst-core` change.

Invoke this skill when:
- the user asks what to run after editing `katalyst-core`
- touched paths span different `katalyst-core` subsystems
- the change may involve generated code, protobufs, or excluded test paths
- you need to explain whether `make test` is sufficient

Do not prefer this skill when:
- the task is read-only and no validation guidance is needed
- the change is outside `katalyst-core`
- a single obvious command already answers the question

## Source Of Truth

Use these repo-local files as the canonical guidance:

- `AGENTS.md`
- `ai/rules/03-build-test-and-ci.md`
- `ai/rules/04-generated-code-and-protos.md`
- `ai/rules/05-testing-patterns.md`

Use the root `Makefile` for the exact command behavior.

## Selection Rules

When using this skill:
1. identify the touched paths under `cmd/`, `pkg/`, or generated surfaces
2. check whether the change touches paths excluded from `make test`
3. choose the smallest command set that still matches repo expectations
4. call out when focused package tests, `make generate`, or binary builds are additionally required
5. state any local-environment limits that prevent full validation

## Required Special Cases

- `make test` excludes `pkg/scheduler`, `pkg/controller/resource-recommend`, and `pkg/util/resource-recommend`
- generated or protobuf changes may need `make generate` or `make generate-pb`
- binary-specific wiring changes may justify `make agent`, `make controller`, `make scheduler`, `make webhook`, or `make metric`

## Output Contract

Return:
1. the touched `katalyst-core` area
2. the recommended validation commands
3. why each command is needed
4. whether `make test` is sufficient or incomplete
5. any generation, build, or environment follow-up
