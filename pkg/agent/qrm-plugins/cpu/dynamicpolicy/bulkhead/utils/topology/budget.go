/*
Copyright 2022 The Katalyst Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package topology

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/deadlock"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	ErrRoundBudgetExceeded                = errors.New("convergence round budget exceeded")
	ErrHierarchyIOOperationBudgetExceeded = errors.New("hierarchy I/O operation budget exceeded")
	ErrNodeBudgetExceeded                 = errors.New("snapshot node budget exceeded")
	ErrHierarchyDepthBudget               = errors.New("hierarchy depth budget exceeded")
	ErrDomainBudgetExceeded               = errors.New("domain budget exceeded")
	ErrTransferEdgeBudgetExceeded         = errors.New("transfer edge budget exceeded")
	ErrPlanOperationBudgetExceeded        = errors.New("plan operation budget exceeded")
	ErrDeadlockProbeBudgetExceeded        = errors.New("deadlock probe operation budget exceeded")
	ErrConvergenceDeadlineExceeded        = errors.New("convergence deadline exceeded")
	ErrAutoCumulativeBudgetInvalid        = errors.New("automatic cumulative budget is invalid")
	ErrAdmissionReservationExceeded       = errors.New("admission reservation exceeded")
)

// ConvergenceBudget bounds all work performed by one coordinator invocation.
// Zero MaxRounds, MaxHierarchyIOOperations, and MaxPlanOperations request
// invocation-scoped automatic limits. Zero means unbounded for other fields.
type ConvergenceBudget struct {
	MaxRounds int
	// MaxHierarchyIOOperations counts logical HierarchyDriver method calls,
	// not implementation syscalls. Retries and explicit verification calls
	// are separate operations.
	MaxHierarchyIOOperations   int
	MaxSnapshotNodes           int
	MaxSnapshotDepth           int
	MaxDomains                 int
	MaxTransferEdges           int
	MaxPlanOperations          int
	MaxDeadlockProbeOperations int
	// AutoDeadlockProbeOperations allows one invocation to raise the default
	// probe limit from the observed snapshot and transfer-atom shape.
	AutoDeadlockProbeOperations bool
	// DeadlineDuration bounds one invocation when the caller does not provide a
	// tighter context deadline. It is converted to an absolute Deadline when a
	// coordinator invocation starts.
	DeadlineDuration time.Duration
	Deadline         time.Time
}

// BudgetUsage is a point-in-time copy of charged logical work.
type BudgetUsage struct {
	Rounds                  int
	HierarchyIOOperations   int
	Nodes                   int
	MaxDepth                int
	Domains                 int
	Edges                   int
	Operations              int
	DeadlockProbeOperations int
}

type PhysicalWriteCost struct {
	CPUSetWrites int
	MemsWrites   int
}

func (c PhysicalWriteCost) Total() int {
	return saturatingAdd(c.CPUSetWrites, c.MemsWrites)
}

func physicalWriteCost(from, to CPUSetTarget, writeMems bool) PhysicalWriteCost {
	cost := PhysicalWriteCost{}
	if !from.CPUs.Equals(to.CPUs) {
		cost.CPUSetWrites = 1
	}
	if writeMems && from.Mems != to.Mems {
		cost.MemsWrites = 1
	}
	return cost
}

func addPhysicalWriteCost(left, right PhysicalWriteCost) PhysicalWriteCost {
	return PhysicalWriteCost{
		CPUSetWrites: saturatingAdd(left.CPUSetWrites, right.CPUSetWrites),
		MemsWrites:   saturatingAdd(left.MemsWrites, right.MemsWrites),
	}
}

func subtractPhysicalWriteCost(left, right PhysicalWriteCost) PhysicalWriteCost {
	return PhysicalWriteCost{
		CPUSetWrites: left.CPUSetWrites - right.CPUSetWrites,
		MemsWrites:   left.MemsWrites - right.MemsWrites,
	}
}

func physicalWriteCostFits(required, available PhysicalWriteCost) bool {
	return required.CPUSetWrites <= available.CPUSetWrites &&
		required.MemsWrites <= available.MemsWrites
}

func (c ExecutionReservationCost) Total() int {
	return saturatingAdd(c.Forward.Total(), c.Rollback.Total())
}

// AutoCumulativeBudgetInput is an invocation-scoped upper bound assembled
// from the initial snapshot and the coordinator's finite round/work limits.
// Totals are cumulative and must not be multiplied by RemainingRounds again.
type AutoCumulativeBudgetInput struct {
	CurrentUsedIO            int
	RemainingRounds          int
	SnapshotIOUpperBound     int
	MaxDrainFrontiersTotal   int
	MaxGrowDomainsTotal      int
	MaxPlanOperationsTotal   int
	MaxChildMembershipsTotal int
	StaleRetryAllowance      int
}

const (
	defaultConvergenceDeadlineDuration = 10 * time.Second
	fixedInvocationHierarchyIOHeadroom = 64
)

const (
	normalSnapshotHierarchyIOPerNode   = 5
	activitySnapshotHierarchyIOPerNode = 7
)

// DefaultConvergenceBudget returns defensive structural limits. Cumulative
// round, hierarchy-I/O, and plan limits are derived after the first snapshot.
func DefaultConvergenceBudget() ConvergenceBudget {
	return ConvergenceBudget{
		MaxRounds:                   0,
		MaxHierarchyIOOperations:    0,
		MaxSnapshotNodes:            defaultApplyMaxSnapshotNodes,
		MaxSnapshotDepth:            defaultApplyMaxSnapshotDepth,
		MaxDomains:                  256,
		MaxTransferEdges:            4096,
		MaxPlanOperations:           0,
		MaxDeadlockProbeOperations:  deadlock.DefaultDeadlockProbeBudget,
		AutoDeadlockProbeOperations: true,
		DeadlineDuration:            defaultConvergenceDeadlineDuration,
	}
}

func NormalizeConvergenceBudget(in ConvergenceBudget) ConvergenceBudget {
	defaults := DefaultConvergenceBudget()
	if in.MaxSnapshotNodes == 0 {
		in.MaxSnapshotNodes = defaults.MaxSnapshotNodes
	}
	if in.MaxSnapshotDepth == 0 {
		in.MaxSnapshotDepth = defaults.MaxSnapshotDepth
	}
	if in.MaxDomains == 0 {
		in.MaxDomains = defaults.MaxDomains
	}
	if in.MaxTransferEdges == 0 {
		in.MaxTransferEdges = defaults.MaxTransferEdges
	}
	if in.MaxDeadlockProbeOperations == 0 {
		in.MaxDeadlockProbeOperations = defaults.MaxDeadlockProbeOperations
		in.AutoDeadlockProbeOperations = true
	}
	if in.DeadlineDuration == 0 {
		in.DeadlineDuration = defaults.DeadlineDuration
	}
	return in
}

func BudgetWithInvocationDeadline(ctx context.Context, in ConvergenceBudget, now time.Time) ConvergenceBudget {
	out := NormalizeConvergenceBudget(in)
	if out.DeadlineDuration > 0 {
		out.Deadline = earliestDeadline(out.Deadline, now.Add(out.DeadlineDuration))
	}
	if deadline, ok := ctx.Deadline(); ok {
		out.Deadline = earliestDeadline(out.Deadline, deadline)
	}
	return out
}

func earliestDeadline(left, right time.Time) time.Time {
	switch {
	case left.IsZero():
		return right
	case right.IsZero():
		return left
	case right.Before(left):
		return right
	default:
		return left
	}
}

type budgetNodeKey struct {
	rel      string
	identity CgroupIdentity
}

// BudgetTracker owns invocation-scoped usage shared by the driver, snapshot,
// planner, writer, and coordinator.
type BudgetTracker struct {
	mu                       sync.Mutex
	limit                    ConvergenceBudget
	usage                    BudgetUsage
	visited                  map[budgetNodeKey]struct{}
	autoHierarchyIOBootstrap bool
}

func NewBudgetTracker(limit ConvergenceBudget) *BudgetTracker {
	return &BudgetTracker{
		limit:   limit,
		visited: make(map[budgetNodeKey]struct{}),
	}
}

func (b *BudgetTracker) EnsureDeadlockProbeCapacity(required int) {
	if b == nil || required <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.limit.AutoDeadlockProbeOperations {
		return
	}
	target := saturatingAdd(b.usage.DeadlockProbeOperations, required)
	if target > b.limit.MaxDeadlockProbeOperations {
		b.limit.MaxDeadlockProbeOperations = target
	}
}

func (b *BudgetTracker) DeadlockProbeLimit() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit.MaxDeadlockProbeOperations
}

func (b *BudgetTracker) AutoDeadlockProbeOperations() bool {
	if b == nil {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit.AutoDeadlockProbeOperations
}

func (b *BudgetTracker) Usage() BudgetUsage {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.usage
}

func (b *BudgetTracker) Deadline() time.Time {
	if b == nil {
		return time.Time{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.limit.Deadline
}

func (b *BudgetTracker) configureAutoHierarchyIOBootstrap(staleRetryAllowance int, collectDormantActivity bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit.MaxHierarchyIOOperations != 0 {
		return nil
	}
	if staleRetryAllowance < 0 {
		return fmt.Errorf("%w: stale retry allowance must not be negative: %d",
			ErrAutoCumulativeBudgetInvalid, staleRetryAllowance)
	}
	perNode := normalSnapshotHierarchyIOPerNode
	if collectDormantActivity {
		// Activity mode proves a v1 leaf with child-list brackets around
		// membership and cpuset reads. Keep bootstrap capacity for the worst
		// logical scan shape before the measured first snapshot can size the
		// cumulative invocation budget.
		perNode = activitySnapshotHierarchyIOPerNode
	}
	snapshotIO, err := checkedAutoBudgetMultiply(b.limit.MaxSnapshotNodes, perNode)
	if err != nil {
		return err
	}
	limit, err := checkedAutoBudgetSum(snapshotIO, staleRetryAllowance, fixedInvocationHierarchyIOHeadroom)
	if err != nil {
		return err
	}
	if limit <= 0 {
		return fmt.Errorf("%w: bootstrap hierarchy I/O limit must be positive", ErrAutoCumulativeBudgetInvalid)
	}
	b.limit.MaxHierarchyIOOperations = limit
	b.autoHierarchyIOBootstrap = true
	return nil
}

func (b *BudgetTracker) configureAutoCumulativeLimitsFromInput(in AutoCumulativeBudgetInput) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.usage.Rounds != 0 {
		return fmt.Errorf("cannot configure cumulative budgets after round use: used=%d", b.usage.Rounds)
	}
	if in.RemainingRounds <= 0 {
		return fmt.Errorf("%w: remaining rounds must be positive: %d", ErrAutoCumulativeBudgetInvalid, in.RemainingRounds)
	}
	for name, value := range map[string]int{
		"current used I/O":         in.CurrentUsedIO,
		"snapshot I/O upper bound": in.SnapshotIOUpperBound,
		"drain frontiers":          in.MaxDrainFrontiersTotal,
		"grow domains":             in.MaxGrowDomainsTotal,
		"plan operations":          in.MaxPlanOperationsTotal,
		"child memberships":        in.MaxChildMembershipsTotal,
		"stale retries":            in.StaleRetryAllowance,
	} {
		if value < 0 {
			return fmt.Errorf("%w: %s must not be negative: %d", ErrAutoCumulativeBudgetInvalid, name, value)
		}
	}
	if in.SnapshotIOUpperBound == 0 {
		return fmt.Errorf("%w: snapshot I/O upper bound must be positive", ErrAutoCumulativeBudgetInvalid)
	}
	if b.limit.MaxHierarchyIOOperations != 0 && !b.autoHierarchyIOBootstrap {
		if b.limit.MaxRounds == 0 {
			b.limit.MaxRounds = in.RemainingRounds
		}
		if b.limit.MaxPlanOperations == 0 {
			b.limit.MaxPlanOperations = in.MaxPlanOperationsTotal
		}
		return nil
	}

	maxSnapshots, err := checkedAutoBudgetSum(
		1,
		in.RemainingRounds,
		in.MaxDrainFrontiersTotal,
		in.MaxGrowDomainsTotal,
		in.StaleRetryAllowance,
	)
	if err != nil {
		return err
	}
	snapshotIO, err := checkedAutoBudgetMultiply(maxSnapshots, in.SnapshotIOUpperBound)
	if err != nil {
		return err
	}
	stableChildScanIO, err := checkedAutoBudgetMultiply(
		estimateStableChildScanHierarchyIO(in.MaxDrainFrontiersTotal, in.MaxChildMembershipsTotal),
		2,
	)
	if err != nil {
		return err
	}
	mutationIO, err := checkedAutoBudgetMultiply(in.MaxPlanOperationsTotal, 5)
	if err != nil {
		return err
	}
	limit, err := checkedAutoBudgetSum(
		in.CurrentUsedIO,
		snapshotIO,
		stableChildScanIO,
		mutationIO,
		fixedInvocationHierarchyIOHeadroom,
	)
	if err != nil {
		return err
	}
	if b.limit.MaxRounds == 0 {
		b.limit.MaxRounds = in.RemainingRounds
	}
	if b.limit.MaxPlanOperations == 0 {
		b.limit.MaxPlanOperations = in.MaxPlanOperationsTotal
	}
	b.limit.MaxHierarchyIOOperations = limit
	b.autoHierarchyIOBootstrap = false
	return nil
}

func checkedAutoBudgetMultiply(left, right int) (int, error) {
	if left < 0 || right < 0 {
		return 0, fmt.Errorf("%w: negative multiplication operand: %d * %d", ErrAutoCumulativeBudgetInvalid, left, right)
	}
	maxInt := int(^uint(0) >> 1)
	if left != 0 && right > maxInt/left {
		return 0, fmt.Errorf("%w: integer overflow multiplying %d by %d", ErrAutoCumulativeBudgetInvalid, left, right)
	}
	return left * right, nil
}

func checkedAutoBudgetSum(values ...int) (int, error) {
	total := 0
	maxInt := int(^uint(0) >> 1)
	for _, value := range values {
		if value < 0 || value > maxInt-total {
			return 0, fmt.Errorf("%w: integer overflow adding %d to %d", ErrAutoCumulativeBudgetInvalid, value, total)
		}
		total += value
	}
	return total, nil
}

func saturatingMultiply(left, right int) int {
	if left <= 0 || right <= 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if left > maxInt/right {
		return maxInt
	}
	return left * right
}

func saturatingAdd(left, right int) int {
	maxInt := int(^uint(0) >> 1)
	if right > 0 && left > maxInt-right {
		return maxInt
	}
	return left + right
}

func (b *BudgetTracker) beforeHierarchyIOOperation(ctx context.Context) error {
	if err := b.checkContextDeadline(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit.MaxHierarchyIOOperations > 0 && b.usage.HierarchyIOOperations >= b.limit.MaxHierarchyIOOperations {
		return fmt.Errorf("%w: limit=%d used=%d", ErrHierarchyIOOperationBudgetExceeded, b.limit.MaxHierarchyIOOperations, b.usage.HierarchyIOOperations)
	}
	b.usage.HierarchyIOOperations++
	return nil
}

func (b *BudgetTracker) checkContextDeadline(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.checkContextDeadlineLocked(ctx)
}

func (b *BudgetTracker) checkContextDeadlineLocked(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !b.limit.Deadline.IsZero() && !time.Now().Before(b.limit.Deadline) {
		return fmt.Errorf("%w: deadline=%s", ErrConvergenceDeadlineExceeded, b.limit.Deadline.Format(time.RFC3339Nano))
	}
	return nil
}

func isConvergenceBudgetError(err error) bool {
	return errors.Is(err, ErrRoundBudgetExceeded) ||
		errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) ||
		errors.Is(err, ErrNodeBudgetExceeded) ||
		errors.Is(err, ErrHierarchyDepthBudget) ||
		errors.Is(err, ErrDomainBudgetExceeded) ||
		errors.Is(err, ErrTransferEdgeBudgetExceeded) ||
		errors.Is(err, ErrPlanOperationBudgetExceeded) ||
		errors.Is(err, ErrDeadlockProbeBudgetExceeded) ||
		errors.Is(err, ErrConvergenceDeadlineExceeded) ||
		errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded)
}

func (b *BudgetTracker) ConsumeDeadlockProbeOperations(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	if err := b.checkContextDeadline(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit.MaxDeadlockProbeOperations > 0 &&
		n > b.limit.MaxDeadlockProbeOperations-b.usage.DeadlockProbeOperations {
		return fmt.Errorf("%w: limit=%d used=%d requested=%d",
			ErrDeadlockProbeBudgetExceeded, b.limit.MaxDeadlockProbeOperations,
			b.usage.DeadlockProbeOperations, n)
	}
	b.usage.DeadlockProbeOperations += n
	return nil
}

func (b *BudgetTracker) VisitNode(rel string, identity CgroupIdentity, depth int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkDepthLocked(depth); err != nil {
		return err
	}
	key := budgetNodeKey{rel: rel, identity: identity}
	if _, ok := b.visited[key]; ok {
		return nil
	}
	if b.limit.MaxSnapshotNodes > 0 && b.usage.Nodes >= b.limit.MaxSnapshotNodes {
		return fmt.Errorf("%w: limit=%d used=%d rel=%q identity=%v", ErrNodeBudgetExceeded, b.limit.MaxSnapshotNodes, b.usage.Nodes, rel, identity)
	}
	b.visited[key] = struct{}{}
	b.usage.Nodes++
	return nil
}

func (b *BudgetTracker) CheckDepth(depth int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.checkDepthLocked(depth)
}

func (b *BudgetTracker) checkDepthLocked(depth int) error {
	if b.limit.MaxSnapshotDepth > 0 && depth > b.limit.MaxSnapshotDepth {
		return fmt.Errorf("%w: limit=%d requested=%d", ErrHierarchyDepthBudget, b.limit.MaxSnapshotDepth, depth)
	}
	if depth > b.usage.MaxDepth {
		b.usage.MaxDepth = depth
	}
	return nil
}

func (b *BudgetTracker) ConsumeRound() error {
	return b.consume(&b.usage.Rounds, b.limit.MaxRounds, 1, ErrRoundBudgetExceeded)
}

func (b *BudgetTracker) ConsumeDomains(count int) error {
	return b.consume(&b.usage.Domains, b.limit.MaxDomains, count, ErrDomainBudgetExceeded)
}

func (b *BudgetTracker) ConsumeTransferEdges(count int) error {
	return b.consume(&b.usage.Edges, b.limit.MaxTransferEdges, count, ErrTransferEdgeBudgetExceeded)
}

func (b *BudgetTracker) ConsumePlanOperations(count int) error {
	return b.consume(&b.usage.Operations, b.limit.MaxPlanOperations, count, ErrPlanOperationBudgetExceeded)
}

// ReserveHierarchyIOOperations preserves the original context-free API.
func (b *BudgetTracker) ReserveHierarchyIOOperations(count int) error {
	return b.ReserveHierarchyIOOperationsWithContext(context.Background(), count)
}

// ReserveHierarchyIOOperationsWithContext charges a known execution upper
// bound before mutation starts. The reserved calls must then use the
// unbudgeted execution path so they are not charged a second time.
func (b *BudgetTracker) ReserveHierarchyIOOperationsWithContext(ctx context.Context, count int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.checkContextDeadlineLocked(ctx); err != nil {
		return err
	}
	if count < 0 {
		return fmt.Errorf("negative budget charge: %d", count)
	}
	maxInt := int(^uint(0) >> 1)
	used := b.usage.HierarchyIOOperations
	limit := b.limit.MaxHierarchyIOOperations
	if used < 0 || count > maxInt-used || limit > 0 && (used > limit || count > limit-used) {
		return fmt.Errorf("%w: limit=%d used=%d requested=%d",
			ErrHierarchyIOOperationBudgetExceeded, limit, used, count)
	}
	b.usage.HierarchyIOOperations += count
	return nil
}

func (b *BudgetTracker) consume(used *int, limit, count int, sentinel error) error {
	if count < 0 {
		return fmt.Errorf("negative budget charge: %d", count)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	maxInt := int(^uint(0) >> 1)
	if *used < 0 || count > maxInt-*used || limit > 0 && (*used > limit || count > limit-*used) {
		return fmt.Errorf("%w: limit=%d used=%d requested=%d", sentinel, limit, *used, count)
	}
	*used += count
	return nil
}

type budgetedHierarchyDriver struct {
	driver HierarchyDriver
	budget *BudgetTracker
	// prepaid is the number of real driver calls charged atomically before a
	// mutation phase starts. Each method call still consumes one prepaid slot.
	prepaidMu sync.Mutex
	prepaid   int
	close     sync.Once
	err       error
}

func NewBudgetedHierarchyDriver(driver HierarchyDriver, budget *BudgetTracker) HierarchyDriver {
	return &budgetedHierarchyDriver{driver: driver, budget: budget}
}

func newReservedBudgetedHierarchyDriver(ctx context.Context, driver HierarchyDriver, budget *BudgetTracker, operations int) (HierarchyDriver, error) {
	if err := budget.ReserveHierarchyIOOperationsWithContext(ctx, operations); err != nil {
		return nil, err
	}
	if wrapped, ok := driver.(*budgetedHierarchyDriver); ok && wrapped.budget == budget {
		driver = wrapped.driver
	}
	return &budgetedHierarchyDriver{driver: driver, budget: budget, prepaid: operations}, nil
}

func (d *budgetedHierarchyDriver) Close() error {
	d.close.Do(func() {
		d.err = d.driver.Close()
	})
	return d.err
}

func (d *budgetedHierarchyDriver) context(ctx context.Context) (context.Context, error) {
	d.prepaidMu.Lock()
	if d.prepaid > 0 {
		if err := d.budget.checkContextDeadline(ctx); err != nil {
			d.prepaidMu.Unlock()
			return nil, err
		}
		d.prepaid--
		d.prepaidMu.Unlock()
		return ctx, nil
	}
	d.prepaidMu.Unlock()
	if err := d.budget.beforeHierarchyIOOperation(ctx); err != nil {
		return nil, err
	}
	return ctx, nil
}

func (d *budgetedHierarchyDriver) Roots(ctx context.Context) ([]RootRef, error) {
	ctx, err := d.context(ctx)
	if err != nil {
		return nil, err
	}
	return d.driver.Roots(ctx)
}

func (d *budgetedHierarchyDriver) StatIdentity(ctx context.Context, rel string) (CgroupIdentity, error) {
	ctx, err := d.context(ctx)
	if err != nil {
		return CgroupIdentity{}, err
	}
	return d.driver.StatIdentity(ctx, rel)
}

func (d *budgetedHierarchyDriver) ReadEntry(ctx context.Context, rel string) (EntryState, error) {
	ctx, err := d.context(ctx)
	if err != nil {
		return EntryState{}, err
	}
	return d.driver.ReadEntry(ctx, rel)
}

func (d *budgetedHierarchyDriver) ReadEntryWithActivity(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
) (EntryState, error) {
	ctx, err := d.context(ctx)
	if err != nil {
		return EntryState{}, err
	}
	reader, ok := d.driver.(hierarchyActivityReader)
	if !ok {
		return EntryState{}, fmt.Errorf("hierarchy driver does not support cgroup activity proofs")
	}
	if reader, ok := d.driver.(budgetedHierarchyActivityReader); ok {
		return reader.readEntryWithActivityAndBudget(ctx, rel, expected, d.budget)
	}
	return reader.ReadEntryWithActivity(ctx, rel, expected)
}

func (d *budgetedHierarchyDriver) ListChildren(ctx context.Context, rel string) ([]ChildRef, error) {
	ctx, err := d.context(ctx)
	if err != nil {
		return nil, err
	}
	if driver, ok := d.driver.(interface {
		listChildrenWithBudget(context.Context, string, *BudgetTracker) ([]ChildRef, error)
	}); ok {
		return driver.listChildrenWithBudget(ctx, rel, d.budget)
	}
	children, err := d.driver.ListChildren(ctx, rel)
	if err != nil {
		return nil, err
	}
	depth := childDepth(rel)
	for _, child := range children {
		if err := d.budget.checkContextDeadline(ctx); err != nil {
			return nil, err
		}
		if err := d.budget.VisitNode(filepath.Join(rel, child.Name), child.Identity, depth); err != nil {
			return nil, err
		}
	}
	return children, nil
}

func childDepth(parentRel string) int {
	clean := filepath.Clean(parentRel)
	if clean == "." {
		return 1
	}
	return len(strings.Split(clean, string(filepath.Separator))) + 1
}

func (d *budgetedHierarchyDriver) WriteCPUs(ctx context.Context, rel string, expected CgroupIdentity, cpus machine.CPUSet) error {
	ctx, err := d.context(ctx)
	if err != nil {
		return err
	}
	return d.driver.WriteCPUs(ctx, rel, expected, cpus)
}

func (d *budgetedHierarchyDriver) WriteMems(ctx context.Context, rel string, expected CgroupIdentity, mems string) error {
	ctx, err := d.context(ctx)
	if err != nil {
		return err
	}
	return d.driver.WriteMems(ctx, rel, expected, mems)
}

func (d *budgetedHierarchyDriver) Classify(err error, op HierarchyOperation) HierarchyErrorClass {
	switch {
	case err == nil:
		return HierarchyErrorNone
	case isConvergenceBudgetError(err):
		return HierarchyErrorBudget
	default:
		return d.driver.Classify(err, op)
	}
}

func (d *budgetedHierarchyDriver) Capabilities() HierarchyCapabilities {
	return d.driver.Capabilities()
}
