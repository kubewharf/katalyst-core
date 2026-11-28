# Katalyst Core Skill Specs

Use the skill specs in this directory for repeated `katalyst-core` workflows that benefit from consistent triggers and output contracts.

These files are repo-local skill specs. Treat them as local source-of-truth documents unless the current harness explicitly exposes them as invocable skills.

## Scope

- Use `rules/` for durable repo policy.
- Use `playbooks/` for step-by-step implementation flows.
- Use `skills/` for repeated decision-making tasks where an agent should return a structured answer.

## How To Use

- If the current harness exposes a matching skill, invoke it directly.
- If not, read the relevant `SKILL.md` as a structured routing or validation guide and return the same output contract manually.
- Prefer the entrypoint finder when the main question is binary, package, or registration ownership.
- Prefer the validation selector when the main question is which commands or focused checks apply.

## Current Skill Specs

- `katalyst-core-validation-selector`
  Choose the right validation commands for touched `katalyst-core` paths.
- `katalyst-core-entrypoint-finder`
  Route a requested behavior change to the correct binary, package, and registration point.

## Maintenance

Keep these files focused on invocation criteria, selection logic, and output contracts. Do not duplicate detailed command lists or implementation steps that already live in `rules/` or `playbooks/`.
