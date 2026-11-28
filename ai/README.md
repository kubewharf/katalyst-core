# Katalyst Core AI Layer

This directory is the repo-local AI layer for `katalyst-core`.

Use `AGENTS.md` as the primary landing page. Use this directory for stable, repo-specific guidance that is easier to reuse than a single long file.

## Layout

- `rules/` captures recurring constraints for structure, extension points, validation, and compatibility.
- `playbooks/` captures common implementation flows.
- `skills/` captures repeated decision-oriented workflows that benefit from structured outputs.

## Scope

Prefer this directory when the task involves:

- locating the right binary or package boundary
- wiring a controller, plugin, webhook, or metric path
- choosing validation commands
- tracing runtime behavior back to extension points

## Start Here

- Use `rules/02-binaries-and-extension-points.md` when you need to place a behavior in the right binary or registration surface.
- Use `playbooks/add-controller-or-plugin.md` for new extension-point work and `playbooks/trace-runtime-behavior.md` for code-path tracing.
- Use `playbooks/update-api-dependency.md` when consuming a new `katalyst-api` change.
- Use `skills/katalyst-core-entrypoint-finder/SKILL.md` or `skills/katalyst-core-validation-selector/SKILL.md` when the main question is routing or validation selection.
- Use `skills/README.md` if you need the local skill-spec usage model, especially the live-skill versus local-spec distinction.

## Maintenance

Do not duplicate shared workspace policy here.

Keep files stable, repository-specific, and free of volatile notes.
