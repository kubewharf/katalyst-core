# B1 Root Cause Audit Notes

## Current HEAD: c0e409bd9

## Freeze condition (line 2459-2463):
```go
if dynamicConf != nil && !dynamicConf.EnableReclaim &&
    commitOverride != nil && commitOverride.ReclaimEffective.IsEmpty() &&
    !committedReclaimC.IsEmpty() {
    commitOverride.ReclaimEffective = committedReclaimC.Clone()
}
```

## Existing converged guard (line 771-788):
```go
if p.pendingAdvisorStateMatchesCommitted(pending) &&
    len(resp.ExtraEntries) == 0 &&
    !p.hasAnyPendingAdvisorPostCommitTarget() {
    // validate, apply migration, return nil (skip commit)
}
```

## B1 Problem:
- When advisor omits reclaim, `reviseReclaimPool` fallback fills ReclaimEffective non-empty
- Freeze condition `ReclaimEffective.IsEmpty()` never triggers
- So `restoredFrozenReclaim` is always false in B1 test scenario
- The existing converged guard at line 771 DOES skip commits for equal frames
- But B1 test scenario produces non-equal entries (fallback fills differently)

## Questions for audit:
1. Should freeze condition be broader (e.g. always restore C when EnableReclaim=false)?
2. Should the converged guard at line 771 be the B1 idempotency mechanism?
3. Is the fallback filling ReclaimEffective the correct behavior when EnableReclaim=false?
