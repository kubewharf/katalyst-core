---
name: "katalyst-core-entrypoint-finder"
description: "Finds the owning binary, package, and registration point for a requested katalyst-core behavior change."
---

# Katalyst Core Entrypoint Finder

Use this skill to route a requested `katalyst-core` behavior change to the correct binary, implementation package, and wiring file.

Invoke this skill when:
- the user asks where a controller, plugin, webhook, metric path, or scheduler behavior should live
- a change request is described in product or behavior terms rather than file paths
- you need to find both implementation and registration points

Do not prefer this skill when:
- the user already named the exact file or package to edit
- the task is outside `katalyst-core`
- the question is only about validation, not ownership or placement

## Source Of Truth

Use these repo-local files as the canonical guidance:

- `AGENTS.md`
- `ai/rules/01-repo-structure.md`
- `ai/rules/02-binaries-and-extension-points.md`
- `ai/playbooks/add-controller-or-plugin.md`
- `ai/playbooks/trace-runtime-behavior.md`

## Selection Rules

When using this skill:
1. identify the owning runtime surface such as agent, controller, scheduler, webhook, or metric
2. locate the likely implementation package under `pkg/`
3. locate the matching registration or options wiring under `cmd/.../app/`
4. call out adjacent config and test locations when they are part of the normal edit path
5. say when the request actually belongs in `katalyst-api` instead of `katalyst-core`

## Output Contract

Return:
1. the owning binary
2. the primary implementation path
3. the registration or wiring path
4. any nearby config or test paths the user should expect to touch
5. any ownership uncertainty or likely cross-repo dependency
