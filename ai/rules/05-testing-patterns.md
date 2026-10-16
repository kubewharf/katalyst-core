# Testing Patterns

Follow local testing conventions when adding or editing runtime logic.

## When To Use

Use this rule when adding tests or deciding how much focused runtime coverage a `katalyst-core` change needs.

## Expectations

- Keep tests next to the code they cover.
- Use `t.Parallel()` in each `t.Run`.
- Prefer focused unit tests first, then broader package coverage if needed.
- Prefer fake metaserver or metacache helpers over overly broad mocks when the repo already has good fakes.
- Cover registration plus one meaningful execution path for new plugins or controllers.

## Common Coverage Targets

- option parsing and config defaults
- registration wiring
- reconcile or allocation core path
- error handling and guardrails
