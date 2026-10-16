# Logging Metrics And Compatibility

Keep behavior changes observable without breaking stable contracts.

## When To Use

Use this rule when a change affects logging style, metric emission, error handling, flags, config keys, or other compatibility-sensitive behavior.

## Logging And Errors

- Prefer `klog.InfoS`, `klog.ErrorS`, and structured keys for new code.
- Keep subsystem prefixes and key names stable.
- Wrap returned errors with `%w`.
- Avoid `panic` in hot paths.

## Metrics

- Emit metrics through existing repo metrics packages.
- Keep tag sets bounded.
- Do not use high-cardinality tags such as pod UID.

## Compatibility

- Preserve flags, config keys, and exported behavior unless a deliberate compatibility plan exists.
- Land shared schema or protocol changes in `katalyst-api` first.
