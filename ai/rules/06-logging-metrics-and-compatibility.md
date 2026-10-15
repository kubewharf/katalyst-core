# Logging Metrics And Compatibility

Keep behavior changes observable without breaking stable contracts.

## When To Use

Use this rule when a change affects logging style, metric emission, error handling, flags, config keys, or other compatibility-sensitive behavior.

## Logging And Errors

- Follow the surrounding package's established `klog` import and call style; both structured and formatted calls are common in this repo.
- Keep subsystem prefixes, message conventions, and structured key names stable.
- Wrap returned errors with `%w`.
- Avoid `panic` in hot paths.

## Metrics

- Emit metrics through existing repo metrics packages.
- Keep tag sets bounded.
- Do not use high-cardinality tags such as pod UID.

## Compatibility

- Preserve flags, config keys, and exported behavior unless a deliberate compatibility plan exists.
- Land public cross-repo schema and plugin contract changes in `katalyst-api` first. Keep established core-owned implementation protocols with their core producers and consumers.
