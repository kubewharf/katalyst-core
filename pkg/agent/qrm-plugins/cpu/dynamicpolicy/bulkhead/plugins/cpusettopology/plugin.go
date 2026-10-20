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

package cpusettopology

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/util/errors"
	v1qos "k8s.io/kubernetes/pkg/apis/core/v1/helper/qos"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	metapod "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const CPUSetTopologyPluginName = "cpuset_topology"

const defaultPendingPodProtectionTTL = 10 * time.Second

var (
	_ bulkheadapi.Plugin                     = (*CPUSetTopologyPlugin)(nil)
	_ bulkheadapi.TopologyPlugin             = (*CPUSetTopologyPlugin)(nil)
	_ bulkheadapi.DisabledTopologyReconciler = (*CPUSetTopologyPlugin)(nil)
	_ bulkheadapi.AdjustmentCapable          = (*CPUSetTopologyPlugin)(nil)
	_ bulkheadapi.PeriodicalCapable          = (*CPUSetTopologyPlugin)(nil)
)

var (
	errReclaimClassificationChanged = errors.New("reclaim path classification changed")
	errPendingPodScopeAmbiguous     = errors.New("pending pod scope is ambiguous")
)

type CPUSetTopologyPlugin struct {
	cfg    bulkheadconfig.BulkheadConfiguration
	cgroup cgroupclient.CgroupClient
	// resolveContainerRel is a test-only override for deterministic synthetic
	// cgroup hierarchies. Production constructors leave it nil and use
	// resolveContainerRelPathFromID directly through resolveContainerRelSafe.
	resolveContainerRel func(podUID, containerID string) (string, error)
	now                 func() time.Time
	pendingProtections  map[string]pendingPodProtection
	modeGateMu          sync.Mutex
	modeGate            *topology.ModeGate

	// resetComplete records whether the authoritative disabled reset has run
	// since the plugin last observed a genuine enabled->disabled transition or a
	// fresh process start/restart. It replaces the manager-side per-name reset-state
	// map: the plugin owns its own transition.
	//
	// Safety contract for startup/restart (preserve=false): the disabled reset
	// writes ONLY the controlled non-leaf DAG nodes (primary/reclaim root, reclaim
	// NUMA buckets, reclaim siblings) to the expand-only whole-machine target.
	// Pod/pause/main-container leaves are never written by reset (kubelet converges
	// them). Because the write set is restricted to controlled non-leaf nodes and the
	// target is expand-only (child ⊆ parent), a restart no longer needs an in-memory
	// ownership witness to safely reset those nodes; resetComplete therefore starts
	// false and the first disabled round performs one idempotent reset (zero writes
	// when already at target).
	resetComplete bool
	// lastEnabled is the previous Enable() result, used to detect the
	// enabled->disabled transition that requires a fresh reset.
	lastEnabled bool
	// lastShouldReconcile is the previous ShouldReconcileWhenDisabled result; a
	// true->false transition requires an authoritative exit reset.
	lastShouldReconcile bool

	// logSink is the per-instance operational log sink. It defaults to the package-level
	// general logger (unchanged production behavior); tests inject a recording sink per
	// plugin instance so log-contract assertions run concurrently without mutating global
	// klog state.
	logSink cpusetTopologyLogSink
}

// cpusetTopologyLogSink abstracts the operational log sink the plugin writes to. The
// default generalLogSink delegates to general.Infof / general.InfofV / general.Errorf
// verbatim; tests supply a recording sink so log assertions are instance-scoped and
// parallel-safe. Errorf is part of the contract so a recording sink can assert a
// message was never emitted at error severity, even though the current routed sites
// are all info-level.
type cpusetTopologyLogSink interface {
	Infof(format string, args ...interface{})
	InfofV(level int, format string, args ...interface{})
	Errorf(format string, args ...interface{})
}

type generalLogSink struct{}

func (generalLogSink) Infof(format string, args ...interface{}) {
	general.Infof(format, args...)
}

func (generalLogSink) InfofV(level int, format string, args ...interface{}) {
	general.InfofV(level, format, args...)
}

func (generalLogSink) Errorf(format string, args ...interface{}) {
	general.Errorf(format, args...)
}

// logs returns the instance log sink, defaulting to the general logger when unset.
func (p *CPUSetTopologyPlugin) logs() cpusetTopologyLogSink {
	if p.logSink != nil {
		return p.logSink
	}
	return generalLogSink{}
}

type pendingPodProtection struct {
	rel          string
	current      machine.CPUSet
	protectUntil time.Time
}

type disabledResetNotConvergedError struct {
	state    topology.ConvergenceState
	applied  int
	deferred int
	failed   int
	report   topology.ConvergenceReport
}

func (e *disabledResetNotConvergedError) Error() string {
	return fmt.Sprintf("disabled reset topology dag not converged: state=%s applied=%d deferred=%d failed=%d report=%+v",
		e.state, e.applied, e.deferred, e.failed, e.report)
}

// Unwrap classifies only a physically successful, deferred-only reset as a safe
// retry. A real write failure or a genuinely non-converged controlled target is
// intentionally left unclassified so the manager continues to fail closed.
func (e *disabledResetNotConvergedError) Unwrap() error {
	if e != nil && e.failed == 0 && e.deferred > 0 && e.report.FullyConverged {
		return bulkheadapi.ErrDisabledResetDeferred
	}
	return nil
}

type topologyApplyNonConvergedError struct {
	result topology.ConvergenceResult
}

func (e *topologyApplyNonConvergedError) Error() string {
	return fmt.Sprintf("apply bulkhead topology dag not converged: state=%s report=%+v",
		e.result.State, e.result.ConvergenceReport)
}

func NewCPUSetTopologyPlugin(conf *config.Configuration) bulkheadapi.Plugin {
	return NewCPUSetTopologyPluginWithCgroupClient(conf, nil)
}

// NewCPUSetTopologyPluginWithCgroupClient injects the cgroup client factory.
// A nil factory defaults to cgroupclient.NewCgroupClient.
func NewCPUSetTopologyPluginWithCgroupClient(
	conf *config.Configuration,
	factory cgroupclient.CgroupClientFactory,
) bulkheadapi.Plugin {
	return NewCPUSetTopologyPluginWithDependencies(conf, factory, nil)
}

// NewCPUSetTopologyPluginWithDependencies is a test-only constructor exported
// solely because integration tests in the sibling bulkhead package exercise the
// real topology plugin against a synthetic cgroup hierarchy. Production wiring
// must use NewCPUSetTopologyPlugin or NewCPUSetTopologyPluginWithCgroupClient and
// must not override container path resolution.
func NewCPUSetTopologyPluginWithDependencies(
	conf *config.Configuration,
	factory cgroupclient.CgroupClientFactory,
	containerRelResolver func(podUID, containerID string) (string, error),
) bulkheadapi.Plugin {
	var cfg bulkheadconfig.BulkheadConfiguration
	if conf != nil && conf.CPUQRMPluginConfig != nil && conf.CPUQRMPluginConfig.BulkheadConfiguration != nil {
		cfg = *conf.CPUQRMPluginConfig.BulkheadConfiguration
	}
	if factory == nil {
		factory = cgroupclient.DefaultCgroupClientFactory()
	}
	plugin := &CPUSetTopologyPlugin{
		cfg:                 cfg,
		cgroup:              factory(),
		resolveContainerRel: containerRelResolver,
		now:                 time.Now,
		pendingProtections:  map[string]pendingPodProtection{},
		modeGate:            topology.NewModeGate(),
	}
	return plugin
}

// resolveContainerRelSafe isolates the test-only resolver override from the
// production path. A nil override is the production invariant and directly
// delegates to resolveContainerRelPathFromID.
func (p *CPUSetTopologyPlugin) resolveContainerRelSafe(podUID, containerID string) (string, error) {
	if p.resolveContainerRel != nil {
		return p.resolveContainerRel(podUID, containerID)
	}
	return resolveContainerRelPathFromID(podUID, containerID)
}

func (p *CPUSetTopologyPlugin) Name() string { return CPUSetTopologyPluginName }

var _ bulkheadapi.CurrentEnabledProvider = (*CPUSetTopologyPlugin)(nil)

// CurrentEnabled is the pure, side-effect-free enable decision: the cgroup v2 gate
// and the config predicate, without touching lastEnabled/resetComplete. The periodic
// gate calls it directly; Enable delegates to it before preserving lifecycle state.
func (p *CPUSetTopologyPlugin) CurrentEnabled(in bulkheadapi.HandlerContext) bool {
	if p.disabledOnCgroupV2(context.Background()) {
		return false
	}
	return enableBulkheadCpusetTopology(in)
}

func (p *CPUSetTopologyPlugin) Enable(in bulkheadapi.HandlerContext) bool {
	curr := p.CurrentEnabled(in)
	if !curr && p.lastEnabled {
		// enabled -> disabled (cgroup v2 gate closed or config off): require a fresh reset.
		p.resetComplete = false
	}
	p.lastEnabled = curr
	return curr
}

func (p *CPUSetTopologyPlugin) ShouldReconcileWhenDisabled(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
) bool {
	// Reclaim preservation engages whenever the opt-in preserve flag is set and
	// full topology management is not owning the round. enableBulkheadCpusetTopology
	// already returns false when shared cores overlap reclaimed cores (overlap
	// itself disables full topology), so the disabled epoch is reached exactly when
	// it matters.
	//
	// This gate intentionally does NOT force preserve off when
	// AllowSharedCoresOverlapReclaimedCores is set. Historically the overlap flag
	// short-circuited to curr=false, which left the entire reclaim subtree inside
	// the disabled reset DAG; buildDisabledResetDAG then stamped the whole-machine
	// reset target onto every reclaim bucket and flattened the running/checkpoint
	// per-NUMA reclaim cpuset (the reclaim roots in BulkheadReclaimRelPaths with their
	// NUMA children under BulkheadReclaimNumaPrefixes; by default the
	// reclaimed root and its reclaimed-* children) to the full node. The new
	// contract preserves that per-NUMA bucket even under shared<->reclaim overlap:
	// the disabled reset DAG excludes the reclaim subtree and a reclaim-only
	// reconcile aligns it to the desired view. cgroup v2 keeps the plugin inert
	// unless explicitly opted in via EnableBulkheadCpusetTopologyOnCgroupV2.
	var curr bool
	if !p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled ||
		enableBulkheadCpusetTopology(in) {
		curr = false
	} else {
		curr = !p.disabledOnCgroupV2(ctx)
	}
	if p.lastShouldReconcile != curr {
		// Entering or leaving the reclaim-only reconcile epoch: a fresh disabled
		// reset is required before the next disabled round.
		p.resetComplete = false
	}
	p.lastShouldReconcile = curr
	return curr
}

// disabledOnCgroupV2 reports whether the cpuset_topology plugin must stay inert
// on the current host. On cgroup v2 the plugin runs only when explicitly opted
// in via EnableBulkheadCpusetTopologyOnCgroupV2; cgroup v1 is never gated. This
// gate is applied at every mutation entry point (Enable, the disabled reset
// handler, and the periodical handler) so a cgroup v2 host is never touched
// when the opt-in is off, even though the manager may still invoke the disabled
// reset handler after a previously-enabled round.
func (p *CPUSetTopologyPlugin) disabledOnCgroupV2(ctx context.Context) bool {
	if p.cfg.EnableBulkheadCpusetTopologyOnCgroupV2 {
		return false
	}
	return p.cgroup.Version(ctx) == cgroupclient.CgroupVersionV2
}

func (p *CPUSetTopologyPlugin) Apply(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
) (out bulkheadapi.TopologyOutcome, err error) {
	start := time.Now()
	defer func() {
		general.Infof("cpuset_topology: plugin apply finished duration=%s err=%v desired_view_nil=%t",
			time.Since(start), err, in.DesiredView == nil)
	}()

	var published *bulkheadapi.TopologyResult
	publish := func(result bulkheadapi.TopologyResult) {
		copied := result
		copied.AppliedView = result.AppliedView.DeepCopy()
		published = &copied
	}

	err = p.runNormalAdjustment(ctx, in, publish)
	var nonConverged *topologyApplyNonConvergedError
	if errors.As(err, &nonConverged) {
		return toOutcome(dagApplyResultFromConvergence(nonConverged.result), outcomeControl{}), nil
	}
	// A topology shadow hold-current is a zero-write defer, not a hard error.
	// The diagnostic error carries the compact hold note; propagate it into the
	// deferred outcome Note so the manager embeds it in ErrTopologyDeferred.
	var holdDiag *shadowHoldDiagnosticError
	if errors.As(err, &holdDiag) {
		general.Infof("cpuset_topology: shadow hold-current deferring cycle err=%v", err)
		return bulkheadapi.TopologyOutcome{Level: bulkheadapi.ConvergenceLevelDeferred, Note: holdDiag.note}, nil
	}
	if errors.Is(err, bulkheadutils.ErrShadowHoldCurrent) {
		general.Infof("cpuset_topology: shadow hold-current deferring cycle err=%v", err)
		return holdCurrentOutcome(), nil
	}
	if err != nil {
		return bulkheadapi.TopologyOutcome{}, err
	}
	if published == nil {
		return toOutcome(topology.DAGApplyResult{
			FullyConverged:       in.DesiredView == nil,
			FinalSnapshotCurrent: in.DesiredView == nil,
			AppliedView:          nil,
		}, outcomeControl{}), nil
	}
	if published.AppliedView == nil {
		return bulkheadapi.TopologyOutcome{}, fmt.Errorf("converged topology result is missing final-snapshot AppliedView")
	}
	return toOutcome(dagApplyResultFromTopologyResult(*published), outcomeControl{}), nil
}

// holdCurrentOutcome is the zero-write defer outcome for a shadow hold-current.
// ConvergenceLevelDeferred makes the manager mark topologyStopped and skip
// dependent workqueue/system consumers this cycle; no AppliedView is attached,
// so no placeholder state is seeded or persisted.
func holdCurrentOutcome() bulkheadapi.TopologyOutcome {
	return bulkheadapi.TopologyOutcome{Level: bulkheadapi.ConvergenceLevelDeferred}
}

// shadowHoldDiagnosticError carries the structured hold-current note so Apply can
// propagate it into TopologyOutcome.Note (and therefore into the
// ErrTopologyDeferred text the manager returns) without re-deriving it. It wraps
// bulkheadutils.ErrShadowHoldCurrent, so the existing bounded zero-write-replay
// classification via errors.Is is unchanged. It is a diagnostic carrier only:
// it changes no hold decision, replan disposition, or write path.
type shadowHoldDiagnosticError struct {
	reason string
	note   string
}

func (e *shadowHoldDiagnosticError) Error() string {
	return fmt.Sprintf("%s: %s", bulkheadutils.ErrShadowHoldCurrent.Error(), e.reason)
}

func (e *shadowHoldDiagnosticError) Unwrap() error { return bulkheadutils.ErrShadowHoldCurrent }

// shadowHoldReclaimBefore returns the pre-fold reclaim reference for the hold
// diagnostic: the business desired reclaim set and the per-NUMA reclaim floor
// decomposition. Both are read straight off the DesiredView and never mutated.
func shadowHoldReclaimBefore(in bulkheadapi.HandlerContext) (reclaimBefore, floorBefore string) {
	if in.DesiredView == nil {
		return "", "{}"
	}
	return in.DesiredView.DesiredReclaimEffective.String(),
		renderNUMASetMap(in.DesiredView.DesiredReclaimEffectivePerNUMA)
}

// buildShadowHoldNote renders the compact one-line diagnostic embedded in
// TopologyOutcome.Note and thus in the ErrTopologyDeferred text. The per-pending
// breakdown and the full per-NUMA floor projection are emitted separately at
// warning level; this stays compact enough to sit inside an error string.
func buildShadowHoldNote(in bulkheadapi.HandlerContext, shadow bulkheadutils.ShadowPartition, protections []topology.PendingProtection) string {
	reclaimBefore, _ := shadowHoldReclaimBefore(in)
	return fmt.Sprintf("hold_reason=%s protected=%s reclaim_before=%s reclaim_after=%s pending=%d",
		shadow.HoldReason, shadow.ProtectedPending.String(),
		reclaimBefore, shadow.ReclaimEffective.String(), len(protections))
}

// renderNUMASetMap renders a NUMA->CPUSet projection deterministically (NUMA ids
// ascending) for the per-NUMA reclaim floor diagnostic. It never mutates the
// input and is used only on the zero-write hold path.
func renderNUMASetMap(m map[int]machine.CPUSet) string {
	if len(m) == 0 {
		return "{}"
	}
	nodes := make([]int, 0, len(m))
	for n := range m {
		nodes = append(nodes, n)
	}
	sort.Ints(nodes)
	var b strings.Builder
	b.WriteByte('{')
	for i, n := range nodes {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%d:%s", n, m[n].String())
	}
	b.WriteByte('}')
	return b.String()
}

// renderReclaimPerNUMA renders the per-NUMA reclaim buckets of a verified
// AppliedView deterministically (sorted NUMA ids, comma-separated with no inner
// whitespace) for the admission view applied audit line. No inner whitespace is
// deliberate: the E2E verifier reads the whole field as one whitespace-delimited
// key=value token, so a space would split and truncate the value.
func renderReclaimPerNUMA(perNUMA map[int]machine.CPUSet) string {
	if len(perNUMA) == 0 {
		return "{}"
	}
	ids := make([]int, 0, len(perNUMA))
	for id := range perNUMA {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	var b strings.Builder
	b.WriteByte('{')
	for i, id := range ids {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "numa%d=%s", id, perNUMA[id].String())
	}
	b.WriteByte('}')
	return b.String()
}

// renderPendingProtectionsDiagnostic renders one structured English segment per
// pending protection: rel/source/CPUs/SharedProjectedCPUs plus the eligible
// classification. eligible is "shared_projected" when the protection carries a
// non-empty SharedProjectedCPUs subset (an eligible non-SNB shared ramp-up
// container clipped onto the shared non-reclaim domain), otherwise "exact"
// (steady/SNB/dedicated/isolation/reclaimed/system folded whole-core). It is
// read-only diagnostics and never changes the fold.
func renderPendingProtectionsDiagnostic(protections []topology.PendingProtection) string {
	if len(protections) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(protections))
	for _, p := range protections {
		eligible := "exact"
		if !p.SharedProjectedCPUs.IsEmpty() {
			eligible = "shared_projected"
		}
		parts = append(parts, fmt.Sprintf("rel=%s source=%s cpus=%s shared_projected=%s eligible=%s",
			p.ScopeRel, p.Source, p.CPUs.String(), p.SharedProjectedCPUs.String(), eligible))
	}
	return "[" + strings.Join(parts, " ; ") + "]"
}

// admissionNonReclaimFallbackAuthority reports which non-reclaim partition the
// soft projection clipped against, mirroring
// bulkheadutils.ProjectPendingProtectionsToDesiredNonReclaim without mutating
// anything. The clip authority is the manager-owned validated non-reclaim
// partition (pre-admission AppliedView NonReclaimPool, or the equivalent
// materialized validated partition folded into the DesiredView); the business
// DesiredNonReclaimPool is business truth and is deliberately not the clip
// authority. Read-only diagnostic.
func admissionNonReclaimFallbackAuthority(in bulkheadapi.HandlerContext) string {
	if !in.ProjectPendingToDesiredNonReclaim {
		return "projection_disabled"
	}
	if in.DesiredView == nil {
		return "no_desired_view"
	}
	if in.AppliedView != nil && !in.AppliedView.NonReclaimPool.IsEmpty() {
		return "applied_validated_non_reclaim_pool"
	}
	if !in.DesiredView.NonReclaimPool.IsEmpty() {
		return "desired_view_validated_non_reclaim_pool"
	}
	return "none_no_validated_domain"
}

// admissionEnforcementBaseline returns the pre-admission validated AppliedView
// partition used to build the enforcement NodeSpecs / required reclaim floors on a
// soft, non-overlap admission. It is nil unless the soft projection gate is on
// (ProjectPendingToDesiredNonReclaim) and a non-nil AppliedView is present, so
// hard/overlap/topology-disabled rounds and the periodic/full path keep deriving
// the shadow purely from the business DesiredView. The returned pointer aliases the
// handler-context AppliedView partition (never mutated); DeriveShadowPartitionWithAppliedEnforcement
// applies the remaining fail-closed guards (non-empty reclaim, reclaim fully inside
// availability) before adopting it.
func admissionEnforcementBaseline(in bulkheadapi.HandlerContext) *model.CPUSetPartitionView {
	if !shouldProjectPendingToValidatedAuthority(in) ||
		in.AppliedView == nil || in.AppliedView.ReclaimEffective.IsEmpty() {
		return nil
	}
	return &in.AppliedView.CPUSetPartitionView
}

// shouldProjectPendingToValidatedAuthority reports whether pending protections
// must be clipped to a non-reclaim authority and the validated applied reclaim
// floor continued. It preserves the historical soft/non-overlap admission gate
// (including the first-ever admission with no AppliedView, which clips against
// the business desired) and additionally engages on a deferred-leaf Retry that
// inherits a manager-owned AppliedView: the ParentSafe admission committed the
// validated reclaim to AppliedView but, by design, not to the persisted business
// state, so rebuilding ancestor floors from the lagging business desired would
// silently drop the bucket the admission had just preserved. Projection engages
// on the manager's non-overlap admission hint (independent of the ramp-up
// hard-partition floor, since the share pool is already disjoint from reclaim)
// or on a deferred-leaf Retry carrying a validated AppliedView; overlap and
// periodical/full rounds without that hint stay on the raw business baseline.
func shouldProjectPendingToValidatedAuthority(in bulkheadapi.HandlerContext) bool {
	if in.ProjectPendingToDesiredNonReclaim {
		return true
	}
	return in.Mode.OrFullDefault() == cpusetutil.CPUSetAdjustmentModeRetry &&
		in.AppliedView != nil && !in.AppliedView.ReclaimEffective.IsEmpty()
}

// emitAdmissionPreConvergeDiagnostic emits the v4-gated structured record of
// every admission's planning-time partition fields, right before the coordinator
// Converge. It is diagnostic-only: it never mutates inputs, selects a hold, or
// feeds the write path. The fields map 1:1 to the decisions that can leave a
// whole-NUMA SNB admission at a bare fixed-point.
func emitAdmissionPreConvergeDiagnostic(
	sink cpusetTopologyLogSink,
	in bulkheadapi.HandlerContext,
	specs []topology.NodeSpec,
	requiredByRel map[string]machine.CPUSet,
	protections []topology.PendingProtection,
	rawProtectionsDiag string,
	rawProtectedDiag string,
	coversProtectedView bool,
	shadow bulkheadutils.ShadowPartition,
	objective topology.ConvergenceObjective,
) {
	if in.DesiredView == nil {
		return
	}
	desiredNonReclaim := in.DesiredView.DesiredNonReclaimPool.String()
	desiredReclaim := in.DesiredView.DesiredReclaimEffective.String()
	// Effective partition the DAG will actually converge: the shadow-derived pair
	// when the topology-only shadow path ran, otherwise the business partition.
	effectiveNonReclaim := in.DesiredView.NonReclaimPool.String()
	effectiveReclaim := in.DesiredView.ReclaimEffective.String()
	effectiveProtected := pendingProtectionCPUSetUnion(protections).String()
	if coversProtectedView {
		effectiveNonReclaim = shadow.NonReclaimPool.String()
		effectiveReclaim = shadow.ReclaimEffective.String()
		effectiveProtected = shadow.ProtectedPending.String()
	}
	// Effective primary and reclaim-NUMA-bucket targets, aggregated from the final
	// node specs the DAG was built from. requiredByRel already pins the reclaim
	// bucket required floors.
	primaryTarget := machine.NewCPUSet()
	reclaimBucketTarget := machine.NewCPUSet()
	for _, sp := range specs {
		switch sp.Role {
		case topology.TopoNodeRolePrimary:
			primaryTarget = primaryTarget.Union(sp.CPUs)
		case topology.TopoNodeRoleReclaimNUMABucket:
			reclaimBucketTarget = reclaimBucketTarget.Union(sp.CPUs)
		}
	}
	sink.InfofV(4, "cpuset_topology: admission pre-converge diagnostic project_pending_to_desired_non_reclaim=%t topology_covers_protected_view=%t non_reclaim_fallback_authority=%s desired_non_reclaim_pool=%s desired_reclaim_effective=%s effective_non_reclaim_pool=%s effective_reclaim=%s pending_raw=%s pending_raw_union=%s pending_projected=%s pending_effective=%s required_by_rel=%s effective_primary_target=%s effective_reclaim_bucket_target=%s objective=%s",
		in.ProjectPendingToDesiredNonReclaim,
		coversProtectedView,
		admissionNonReclaimFallbackAuthority(in),
		desiredNonReclaim,
		desiredReclaim,
		effectiveNonReclaim,
		effectiveReclaim,
		rawProtectionsDiag,
		rawProtectedDiag,
		renderPendingProtectionsDiagnostic(protections),
		effectiveProtected,
		formatCPUSetByRel(requiredByRel),
		primaryTarget.String(),
		reclaimBucketTarget.String(),
		string(objective),
	)
}

// renderRelConvergences renders the non-converged-target deficits observed on a
// no-progress admission round. Read-only diagnostic.
func renderRelConvergences(targets []topology.RelConvergence) string {
	if len(targets) == 0 {
		return "none"
	}
	rels := make([]string, 0, len(targets))
	for _, t := range targets {
		rels = append(rels, fmt.Sprintf("rel=%s observed=%s target=%s reason=%s",
			t.Rel, t.Observed.String(), t.Target.String(), t.Reason))
	}
	return "[" + strings.Join(rels, " ; ") + "]"
}

func dagApplyResultFromTopologyResult(result bulkheadapi.TopologyResult) topology.DAGApplyResult {
	return topology.DAGApplyResult{
		Attempted:            result.Attempted,
		Applied:              result.Applied,
		Skipped:              result.Skipped,
		Failed:               result.Failed,
		Deferred:             result.Deferred,
		FullyConverged:       result.Converged,
		ParentSafe:           result.ParentSafe,
		DeferredLeafCount:    result.DeferredLeafCount,
		DeferredCPUCount:     result.DeferredCPUCount,
		FinalSnapshotCurrent: result.FinalSnapshotCurrent,
		AppliedView:          result.AppliedView.DeepCopy(),
	}
}

func dagApplyResultFromConvergence(result topology.ConvergenceResult) topology.DAGApplyResult {
	out := topology.DAGApplyResult{
		Attempted:            result.Attempted,
		Applied:              result.Applied,
		Skipped:              result.Skipped,
		Failed:               result.Failed,
		Deferred:             result.Deferred,
		FullyConverged:       result.Converged,
		ParentSafe:           result.ParentSafe,
		DeferredLeafCount:    result.DeferredLeafCount,
		DeferredCPUCount:     result.DeferredCPUCount,
		FinalSnapshotCurrent: result.FinalSnapshotCurrent,
		ConvergenceReport:    result.ConvergenceReport,
	}
	return out
}

func topologyResultFromFinalConvergence(
	result topology.ConvergenceResult,
	appliedView *model.AppliedView,
) bulkheadapi.TopologyResult {
	applied := appliedView.DeepCopy()
	if applied != nil {
		applied.Level = model.AppliedViewLevelFull
		if result.ParentSafe {
			applied.Level = model.AppliedViewLevelParentSafe
		}
	}
	return bulkheadapi.TopologyResult{
		Attempted:            result.Attempted,
		Applied:              result.Applied,
		Skipped:              result.Skipped,
		Failed:               result.Failed,
		Deferred:             result.Deferred,
		Converged:            result.Converged,
		ParentSafe:           result.ParentSafe,
		LeafDeferred:         result.ParentSafe && result.DeferredLeafCount > 0,
		DeferredLeafCount:    result.DeferredLeafCount,
		DeferredCPUCount:     result.DeferredCPUCount,
		FinalSnapshotCurrent: result.FinalSnapshotCurrent,
		AppliedView:          applied,
	}
}

type topologyAdjustmentAttempt func(
	context.Context,
	bulkheadapi.HandlerContext,
	*topology.AdjustmentBudget,
) (topology.ConvergenceResult, error)

func (p *CPUSetTopologyPlugin) CPUSetAdjustmentHandler(ctx context.Context, in bulkheadapi.HandlerContext) error {
	return p.runNormalAdjustment(ctx, in, nil)
}

// runNormalAdjustment runs the normal DesiredView -> AppliedView convergence.
// publish, when non-nil, receives the final converged TopologyResult exactly
// once (when FinalSnapshotCurrent holds); it is the internal handoff that used
// to travel through HandlerContext.ReportTopologyResult.
func (p *CPUSetTopologyPlugin) runNormalAdjustment(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
	publish func(bulkheadapi.TopologyResult),
) error {
	handlerStartedAt := time.Now()
	if in.Mode.OrFullDefault() == cpusetutil.CPUSetAdjustmentModeAdmission &&
		p.cfg.AdmissionSafeDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithDeadline(ctx, handlerStartedAt.Add(p.cfg.AdmissionSafeDuration))
		defer cancel()
	}
	if in.DesiredView == nil {
		return nil
	}
	budget := topology.NewAdjustmentBudget(
		ctx, topologyBudgetFromConfig(p.cfg.TopologyConvergenceBudget))
	attempt := func(ctx context.Context, in bulkheadapi.HandlerContext, b *topology.AdjustmentBudget) (topology.ConvergenceResult, error) {
		var result topology.ConvergenceResult
		err := p.adjustOnceWithResult(ctx, in, b, &result, publish)
		return result, err
	}
	_, err := runCPUSetTopologyAdjustmentWithResult(ctx, in, budget, attempt)
	scheduleFixedPointRetry(in.Mode, err, in.ScheduleFullRetry)
	return err
}

func runCPUSetTopologyAdjustment(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
	budget *topology.AdjustmentBudget,
	attempt topologyAdjustmentAttempt,
) error {
	_, err := runCPUSetTopologyAdjustmentWithResult(ctx, in, budget, attempt)
	return err
}

func runCPUSetTopologyAdjustmentWithResult(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
	budget *topology.AdjustmentBudget,
	attempt topologyAdjustmentAttempt,
) (topology.ConvergenceResult, error) {
	if in.DesiredView == nil {
		return topology.ConvergenceResult{}, nil
	}
	pristineDesired := in.DesiredView.DeepCopy()
	var cumulative topology.ConvergenceResult
	var lastStale error
	terminal := func(err error) (topology.ConvergenceResult, error) {
		cumulative.Converged = false
		cumulative.ParentSafe = false
		cumulative.State = topology.ConvergenceStateNonConverged
		cumulative.FinalSnapshot = nil
		cumulative.FinalSnapshotCurrent = false
		cumulative.ConvergenceReport.FullyConverged = false
		cumulative.DeferredLeafCount = 0
		cumulative.DeferredCPUCount = 0
		cumulative.Published = false
		cumulative.ReplanDisposition = topology.ReplanNotAllowed
		return cumulative, err
	}
	iteration := 0
	for {
		if err := ctx.Err(); err != nil {
			return terminal(budget.ExhaustionError(topology.ErrAdjustmentDeadlineExceeded, lastStale))
		}
		iteration++
		attemptInput := in
		attemptInput.DesiredView = pristineDesired.DeepCopy()
		result, err := attempt(ctx, attemptInput, budget)
		accumulateConvergenceResult(&cumulative, result)
		general.InfofV(4, "cpuset_topology: adjustment attempt finished iteration=%d disposition=%d err=%v",
			iteration, result.ReplanDisposition, err)
		deadlinePrimary, deadlineHistory := err, lastStale
		if deadlinePrimary == nil {
			deadlinePrimary, deadlineHistory = lastStale, nil
		}
		if deadlineErr := budget.DeadlineErrorWithHistory(
			deadlinePrimary, deadlineHistory); deadlineErr != nil {
			return terminal(deadlineErr)
		}
		if err == nil {
			return cumulative, nil
		}
		if errors.Is(err, topology.ErrAdjustmentWriteBudgetExceeded) {
			if lastStale == nil {
				lastStale = err
			}
			return terminal(budget.ExhaustionError(topology.ErrAdjustmentWriteBudgetExceeded, lastStale))
		}
		if !budget.ReplanSafe(result.ReplanDisposition) {
			return terminal(err)
		}
		lastStale = err
		if result.ReplanDisposition == topology.ReplanSafeFromVerifiedFinalState {
			if !result.FinalSnapshotCurrent || result.FinalSnapshot == nil {
				return terminal(err)
			}
			budget.StartFromVerifiedFinalSnapshot(result.FinalSnapshot)
		} else {
			budget.ClearInitialSnapshot()
		}
		if exhaustionErr := budget.ConsumeReplan(err); exhaustionErr != nil {
			return terminal(exhaustionErr)
		}
	}
}

func (p *CPUSetTopologyPlugin) adjustOnceWithResult(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
	adjustmentBudget *topology.AdjustmentBudget,
	attemptResult *topology.ConvergenceResult,
	publish func(bulkheadapi.TopologyResult),
) error {
	relExists := func(rel string) error {
		_, err := p.cgroup.StatDir(ctx, rel)
		return err
	}
	buildExpectedStart := time.Now()
	expectedRes, err := p.buildExpectedCPUSetByRel(ctx, in)
	if expectedRes == nil {
		expectedRes = &expectedCPUSetBuildResult{}
	}
	general.Infof("cpuset_topology: build expected cpuset finished duration=%s err=%v expected_leaf_count=%d pending_count=%d pending_cpu_count=%d",
		time.Since(buildExpectedStart), err, len(expectedRes.ExpectedByRel), len(expectedRes.PendingByPod), expectedRes.PendingCPUSetUnion().Size())
	if deadlineErr := admissionStageDeadlineError(ctx, "build expected container cpuset"); deadlineErr != nil {
		return deadlineErr
	}
	if err != nil {
		// Only non-pending resolve failures (illegal rel, cgroup/metaserver
		// internal error) reach here. Pending containers (admit window, no
		// container id yet) are classified as protected-pending and do NOT
		// produce an error, so a normal new-pod admit is never rejected.
		emitBulkheadPruneResult(in.Emitter, "skipped", "container_error")
		return fmt.Errorf("build expected container cpuset: %w", err)
	}
	discoveredSiblings, err := p.discoverBulkheadReclaimSiblings(ctx, in.DesiredView)
	if deadlineErr := admissionStageDeadlineError(ctx, "discover bulkhead reclaim siblings"); deadlineErr != nil {
		return deadlineErr
	}
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", "discover_error")
		return fmt.Errorf("discover bulkhead reclaim siblings: %w", err)
	}
	configuredSiblings := p.configuredBulkheadReclaimSiblings()
	siblings := p.mergeBulkheadReclaimSiblings(
		discoveredSiblings,
		configuredSiblings,
		desiredCPUSetPartitionView(in.DesiredView),
	)
	general.InfofV(2, "bulkhead: reclaim sibling resolution (admit) configured=%v discovered=%v merged=%v system_rel=%q",
		configuredSiblings, discoveredSiblings, siblings, p.cfg.BulkheadSystemRelPath)
	var cpuDetails machine.CPUDetails
	if in.Topology != nil {
		cpuDetails = in.Topology.CPUDetails
	}
	specs, err := bulkheadutils.BuildTopologyNodeSpecsFromView(p.cfg, desiredCPUSetPartitionView(in.DesiredView), cpuDetails, siblings, relExists)
	if deadlineErr := admissionStageDeadlineError(ctx, "build bulkhead topology inputs"); deadlineErr != nil {
		return deadlineErr
	}
	if err != nil {
		return fmt.Errorf("build bulkhead topology inputs: %w", err)
	}
	{
		relRoles := make([]string, 0, len(specs))
		for _, sp := range specs {
			relRoles = append(relRoles, sp.Rel+":"+string(sp.Role))
		}
		general.InfofV(2, "bulkhead: topology node specs (admit) %v", relRoles)
	}
	dag, err := topology.BuildDAG(specs)
	if deadlineErr := admissionStageDeadlineError(ctx, "build bulkhead topology dag"); deadlineErr != nil {
		return deadlineErr
	}
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", "dag_error")
		return fmt.Errorf("build bulkhead topology dag: %w", err)
	}
	protections, err := p.pendingProtectionScopes(ctx, dag, expectedRes.PendingByPod)
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", "pending_scope_error")
		return fmt.Errorf("resolve pending protection scopes: %w", err)
	}
	// DIAGNOSTIC ONLY: render the pending protection set verbatim, before the soft
	// non-overlap projection below can clip it onto the shared non-reclaim domain.
	// The projected (post-projection) and effective (whole-core folded) renders are
	// emitted alongside this in the pre-Converge record. Read-only; does not feed
	// any hold decision, replan disposition, or write path.
	rawProtectionsDiag := renderPendingProtectionsDiagnostic(protections)
	rawProtectedDiag := pendingProtectionCPUSetUnion(protections).String()
	if shouldProjectPendingToValidatedAuthority(in) {
		// Clip pending to the validated non-reclaim authority. The first-ever
		// admission (no AppliedView) clips against the business desired; a
		// deferred-leaf Retry inherits the manager-owned AppliedView so its
		// ancestor reclaim floors continue from the validated effective protection
		// rather than the lagging business desired.
		protections = bulkheadutils.ProjectPendingProtectionsToDesiredNonReclaim(
			in.DesiredView, in.Topology, protections, in.AppliedView)
	}
	if err := p.ensureBulkheadReclaimSiblingDirs(ctx, siblings); err != nil {
		return fmt.Errorf("ensure bulkhead reclaim sibling cgroups: %w", err)
	}
	protectedPending := pendingProtectionCPUSetUnion(protections)
	protectedByRel := pendingProtectedCPUSetByResolvedScopes(protections)
	if deadlineErr := admissionStageDeadlineError(ctx, "protect pending container cpuset"); deadlineErr != nil {
		return deadlineErr
	}
	// DIAGNOSTIC ONLY: hoist the cover check and shadow partition so the
	// pre-Converge record can report them on every admission. The condition value
	// is unchanged; control flow is identical.
	coversProtectedView := topologyCoversProtectedView(in.Topology, in.DesiredView,
		protectedPending.Union(unionCPUSetByRel(protectedByRel)))
	var shadow bulkheadutils.ShadowPartition
	if coversProtectedView {
		// Topology-only shadow: derive the planning partition as a pure function
		// of the business DesiredView, topology and pending protections. The
		// business DesiredView is never mutated; pending CPUs are folded onto the
		// non-reclaim side BEFORE the per-NUMA reclaim floor is projected, so
		// non-reclaim and reclaim stay complementary and disjoint.
		shadow = bulkheadutils.DeriveShadowPartitionWithAppliedEnforcement(
			in.DesiredView, in.Topology, protections, admissionEnforcementBaseline(in))
		general.InfofV(5, "bulkhead: shadow partition pending_count=%d protected=%s non_reclaim=%s reclaim=%s hold=%v reason=%s shared_domain=%s shared_projected=%s err=%v",
			len(expectedRes.PendingByPod), shadow.ProtectedPending.String(),
			shadow.NonReclaimPool.String(), shadow.ReclaimEffective.String(),
			shadow.HoldCurrent, shadow.HoldReason,
			shadow.SharedDomain.String(), shadow.SharedProjected.String(), shadow.Err)
		if shadow.Err != nil {
			// Typed, fail-closed projection contradiction: zero physical writes and
			// NOT a bounded Deferred replay. Surface the error so the manager decides
			// retry/reject policy without emitting an empty/unsound reclaim target.
			general.Warningf("bulkhead: shared pending projection failed closed, zero writes, err=%v pending=%s domain=%s projected=%s",
				shadow.Err, shadow.ProtectedPending.String(), shadow.SharedDomain.String(), shadow.SharedProjected.String())
			emitBulkheadPruneResult(in.Emitter, "failed", "shared_projection")
			return fmt.Errorf("bulkhead shared pending projection: %w", shadow.Err)
		}
		if shadow.HoldCurrent {
			// Zero-write short-circuit: do not feed an empty reclaim desired onto
			// non-empty current reclaim cgroups. Hold-current is terminal on the
			// first attempt: do not burn the replan budget. Apply maps the
			// sentinel to ConvergenceLevelDeferred, and the manager surfaces
			// ErrTopologyDeferred to the existing upper layer, which decides
			// retry/reject policy.
			//
			// Diagnostics only: emit a warning-level, structured English record of
			// why the hold fired (HoldReason, protected set, reclaim before/after,
			// per-NUMA reclaim floor before/after, and each pending protection's
			// rel/source/CPUs/SharedProjectedCPUs/eligible classification) and
			// carry the compact note on the returned error so Apply propagates it
			// into TopologyOutcome.Note. This changes no hold decision, replan
			// disposition, or write path.
			note := buildShadowHoldNote(in, shadow, protections)
			reclaimBefore, floorBefore := shadowHoldReclaimBefore(in)
			general.Warningf("cpuset_topology: shadow hold-current zero-write defer reason=%s protected=%s hard_set=%s reclaim_before=%s reclaim_after=%s floor_before=%s floor_after=%s pending=%s",
				shadow.HoldReason,
				shadow.ProtectedPending.String(),
				shadow.ExcludedHardSet.String(),
				reclaimBefore,
				shadow.ReclaimEffective.String(),
				floorBefore,
				renderNUMASetMap(shadow.ReclaimEffectivePerNUMA),
				renderPendingProtectionsDiagnostic(protections))
			emitBulkheadPruneResult(in.Emitter, "deferred", shadow.HoldReason)
			if attemptResult != nil {
				attemptResult.ReplanDisposition = topology.ReplanNotAllowed
				attemptResult.State = topology.ConvergenceStateNonConverged
			}
			return &shadowHoldDiagnosticError{reason: shadow.HoldReason, note: note}
		}
		shadowView := shadow.AsPartitionView(&in.DesiredView.CPUSetPartitionView)
		specs, err = bulkheadutils.BuildTopologyNodeSpecsFromView(p.cfg, shadowView, cpuDetails, siblings, relExists)
		if err != nil {
			return fmt.Errorf("build shadow bulkhead topology inputs: %w", err)
		}
		dag, err = topology.BuildDAG(specs)
		if err != nil {
			return fmt.Errorf("build shadow bulkhead topology dag: %w", err)
		}
	}
	if in.Mode.OrFullDefault() == cpusetutil.CPUSetAdjustmentModeAdmission {
		effectiveReclaim := in.DesiredView.DesiredReclaimEffective
		if coversProtectedView {
			effectiveReclaim = shadow.ReclaimEffective
		}
		p.reclassifyAdmissionDeferredLeaves(ctx, in, effectiveReclaim, expectedRes)
	}
	requiredCPUSetByRel := topology.RequiredCPUSetByRelFromNodeSpecs(specs)
	general.InfofV(5, "cpuset_topology: apply start specs=%d siblings=%d expected_leaf_count=%d pending_count=%d protected_pending=%s protected_rel_count=%d",
		len(specs), len(siblings), len(expectedRes.ExpectedByRel), len(protections),
		protectedPending.String(), len(protectedByRel))
	reservedCPUSet := in.DesiredView.Reserve
	objective := topology.ConvergenceObjectiveFull
	if in.Mode.OrFullDefault() == cpusetutil.CPUSetAdjustmentModeAdmission {
		objective = topology.ConvergenceObjectiveParentSafe
	}
	// DIAGNOSTIC ONLY: on every admission, right before the coordinator Converge,
	// emit a v4-gated structured record of the fields that decide whether this
	// round can make progress. This is read-only: it recomputes no decisions and
	// feeds no hold/replan/write path. It exists to explain a bare fixed-point
	// no-progress (the reclaimed-0 bucket never materializes) at runtime.
	emitAdmissionPreConvergeDiagnostic(p.logs(), in, specs, requiredCPUSetByRel, protections,
		rawProtectionsDiag, rawProtectedDiag, coversProtectedView, shadow, objective)
	// Normal adjustment passes the topology explicitly so TopologyCoordinator can
	// derive its allowed CPUs from this round's machine view. Any apply error is
	// returned to the bulkhead manager through this handler; this plugin does
	// not attempt a local retry or partial recovery.
	convergeStart := time.Now()
	var finalAppliedView *model.AppliedView
	convergenceBudget := adjustmentBudget.RemainingConvergenceBudget()
	res, err := (topology.TopologyCoordinator{}).Converge(ctx, topology.CoordinatorInput{
		DAG:                 dag,
		Cgroup:              p.cgroup,
		Mode:                topology.NormalModeGuardWithGate(p.sharedModeGate()),
		Budget:              convergenceBudget,
		AdjustmentBudget:    adjustmentBudget,
		DrainSelection:      topologyDrainSelectionFromConfig(p.cfg.TopologyDrainSelection),
		CPUDetails:          cpuDetails,
		ReservedCPUSet:      reservedCPUSet,
		InitialSnapshot:     adjustmentBudget.InitialSnapshot(),
		ExpectedCPUSetByRel: expectedRes.ExpectedByRel,
		RequiredCPUSetByRel: requiredCPUSetByRel,
		Objective:           objective,
		DeferredCPUSetByRel: expectedRes.DeferredLeafByRel,
		AdmissionBudget: &topology.AdmissionConvergenceBudget{
			MaxRequiredWrites: p.cfg.AdmissionMaxRequiredWrites,
		},
		PendingProtections:   protections,
		ProtectedCPUSetByRel: protectedByRel,
		PublishFinalSnapshot: func(snapshot *topology.CompleteSnapshot) error {
			appliedView, err := appliedViewFromFinalSnapshotWithContext(
				ctx, in.MetaServer, in.DesiredView, dag, snapshot,
				expectedRes.LifecycleProofs, nil)
			if err != nil {
				return fmt.Errorf("derive applied view from final topology snapshot: %w", err)
			}
			finalAppliedView = appliedView
			return nil
		},
		PublishParentSafeSnapshot: func(snapshot *topology.CompleteSnapshot, deferredCleanupRels map[string]struct{}) error {
			appliedView, err := appliedViewFromFinalSnapshotWithDeferredCleanup(
				ctx, in.MetaServer, in.DesiredView, dag, snapshot, deferredCleanupRels,
				expectedRes.LifecycleProofs)
			if err != nil {
				return fmt.Errorf("derive parent-safe applied view from final topology snapshot: %w", err)
			}
			finalAppliedView = appliedView
			return nil
		},
	})
	if attemptResult != nil {
		*attemptResult = res
	}
	general.Infof("cpuset_topology: coordinator converge finished duration=%s err=%v attempted=%d applied=%d skipped=%d failed=%d deferred=%d converged=%t final_snapshot_current=%t state=%s expected_leaf_count=%d pending_count=%d pending_cpu_count=%d protected_rel_count=%d specs=%d siblings=%d",
		time.Since(convergeStart), err, res.Attempted, res.Applied, res.Skipped, res.Failed, res.Deferred,
		res.Converged, res.FinalSnapshotCurrent, res.State, len(expectedRes.ExpectedByRel), len(protections),
		protectedPending.Size(), len(protectedByRel), len(specs), len(siblings))
	if err != nil {
		emitBulkheadTopologySummary(in.Emitter, "normal", res, err)
		emitBulkheadPruneResult(in.Emitter, "skipped", "dag_error")
		return fmt.Errorf("apply bulkhead topology dag: %w", err)
	}
	if (res.Converged || res.ParentSafe) && res.FinalSnapshotCurrent {
		if finalAppliedView == nil {
			return fmt.Errorf("final convergence result is missing final-snapshot AppliedView")
		}
		if publish != nil {
			publish(topologyResultFromFinalConvergence(res, finalAppliedView))
		}
		// Low-noise structured audit of a verified admission AppliedView. It fires
		// only on a successful admission-mode convergence (never on periodic/full
		// rounds) and stamps the process pid so the E2E verifier can fence to the
		// current agent process instead of matching fragile free text. Read-only:
		// it selects no hold, mutates nothing, and the return path is unchanged.
		if in.Mode.OrFullDefault() == cpusetutil.CPUSetAdjustmentModeAdmission {
			p.logs().Infof("cpuset_topology: admission view applied pid=%d level=%s attempted=%d applied=%d converged=%t parent_safe=%t final_snapshot_current=%t state=%s applied_reclaim_effective=%s applied_reclaim_per_numa=%s",
				os.Getpid(), finalAppliedView.Level, res.Attempted, res.Applied,
				res.Converged, res.ParentSafe, res.FinalSnapshotCurrent, res.State,
				finalAppliedView.ReclaimEffective.String(), renderReclaimPerNUMA(finalAppliedView.ReclaimEffectivePerNUMA))
		}
	}
	emitBulkheadTopologySummary(in.Emitter, "normal", res, nil)
	if !res.Converged && !res.ParentSafe {
		// DIAGNOSTIC ONLY: no-progress round. Emit the convergence/parent-safety
		// deficits this pass observed (pending-to-primary/reclaim residuals,
		// non-converged targets observed-vs-target) so a bare fixed-point stall is
		// attributable at runtime. Read-only; the return below is unchanged.
		general.Errorf("cpuset_topology: admission no-progress deficits state=%s attempted=%d applied=%d skipped=%d failed=%d deferred=%d pending_to_primary=%s pending_to_reclaim=%s cleanup_pending_primary=%s cleanup_pending_reclaim=%s non_converged_targets=%s",
			res.State, res.Attempted, res.Applied, res.Skipped, res.Failed, res.Deferred,
			res.ConvergenceReport.PendingToPrimary.String(), res.ConvergenceReport.PendingToReclaim.String(),
			res.ConvergenceReport.CleanupPendingPrimary.String(), res.ConvergenceReport.CleanupPendingReclaim.String(),
			renderRelConvergences(res.ConvergenceReport.NonConvergedTargets))
		general.InfofV(4, "cpuset_topology: apply not fully converged, deferred=%d state=%s report=%+v", res.Deferred, res.State, res.ConvergenceReport)
		reason := "not_converged"
		if res.Deferred > 0 {
			reason = "deferred_convergence"
		}
		emitBulkheadPruneResult(in.Emitter, "skipped", reason)
		return &topologyApplyNonConvergedError{result: res}
	}
	if res.ParentSafe && len(expectedRes.DeferredLeafByRel) > 0 {
		if err := handleDeferredLeafRetry(in.Mode, in.ScheduleFullRetry); err != nil {
			return err
		}
	}

	activeRels := bulkheadutils.CollectActiveRels(p.cfg, desiredCPUSetPartitionView(in.DesiredView), in.MetaServer, siblings, relExists)
	p.cgroup.Prune(activeRels)
	emitBulkheadPruneResult(in.Emitter, "success", "")
	emitBulkheadPruneActiveRels(in.Emitter, len(activeRels), "success", "")
	return nil
}

func (p *CPUSetTopologyPlugin) ensureBulkheadReclaimSiblingDirs(ctx context.Context, siblings []string) error {
	for _, rel := range siblings {
		rel = strings.Trim(rel, "/")
		if rel == "" {
			continue
		}
		if err := p.cgroup.EnsureDir(ctx, rel); err != nil {
			return fmt.Errorf("ensure reclaim sibling rel path %q: %w", rel, err)
		}
	}
	return nil
}

func scheduleFixedPointRetry(
	mode cpusetutil.CPUSetAdjustmentMode,
	err error,
	schedule func(cpusetutil.CPUSetAdjustmentRetryReason),
) {
	// The current admission remains fail-closed. Only enqueue one fresh-state Full
	// retry; Retry/Periodic failures stay dirty in the existing bounded retry loop
	// and must not recursively schedule themselves.
	if mode.OrFullDefault() == cpusetutil.CPUSetAdjustmentModeAdmission &&
		errors.Is(err, topology.ErrNoProgress) && schedule != nil {
		schedule(cpusetutil.RetryReasonFixedPointStall)
	}
}

func handleDeferredLeafRetry(
	mode cpusetutil.CPUSetAdjustmentMode,
	schedule func(cpusetutil.CPUSetAdjustmentRetryReason),
) error {
	if mode == cpusetutil.CPUSetAdjustmentModeRetry {
		return fmt.Errorf("deferred cpuset leaf is still pending")
	}
	if schedule != nil {
		schedule(cpusetutil.RetryReasonDeferredLeaf)
	}
	return nil
}

func admissionStageDeadlineError(ctx context.Context, stage string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%s: %w", stage, err)
	}
	return nil
}

func appliedViewFromFinalSnapshotWithContext(
	ctx context.Context,
	metaServer *metaserver.MetaServer,
	desired *model.DesiredView,
	dag *topology.TopoDAG,
	snapshot *topology.CompleteSnapshot,
	lifecycleProofs containerLifecycleProofSet,
	deferredCleanupRels map[string]struct{},
) (*model.AppliedView, error) {
	return appliedViewFromFinalSnapshotWithDeferredCleanup(
		ctx, metaServer, desired, dag, snapshot, deferredCleanupRels, lifecycleProofs)
}

func appliedViewFromFinalSnapshotWithDeferredCleanup(
	ctx context.Context,
	metaServer *metaserver.MetaServer,
	desired *model.DesiredView,
	dag *topology.TopoDAG,
	snapshot *topology.CompleteSnapshot,
	deferredCleanupRels map[string]struct{},
	lifecycleProofs containerLifecycleProofSet,
) (*model.AppliedView, error) {
	if desired == nil || dag == nil || snapshot == nil {
		return nil, fmt.Errorf("desired view, topology dag and final snapshot are required")
	}
	partition := model.NewCPUSetPartitionView()
	partition.Dedicated = desired.Dedicated.Clone()
	for poolName, cpus := range desired.SharePoolMap {
		partition.SharePoolMap[poolName] = cpus.Clone()
	}
	applied := &model.AppliedView{
		CPUSetPartitionView: partition,
		Level:               model.AppliedViewLevelFull,
		CPUSetByRel:         make(map[string]machine.CPUSet, len(dag.Nodes())),
		RelProofByRel:       make(map[string]model.CgroupRelProof, len(dag.Nodes())),
		PoolProjection:      model.NewAppliedPoolProjection(),
	}
	applied.ReclaimEffectivePerNUMA = map[int]machine.CPUSet{}
	for _, node := range dag.Nodes() {
		proof, ok := snapshot.TargetProofCPUs(node.Rel, node.CPUs)
		if !ok {
			return nil, fmt.Errorf("final snapshot misses controlled rel %q", node.Rel)
		}
		applied.CPUSetByRel[node.Rel] = proof.Clone()
		entry := snapshot.Entries[node.Rel]
		applied.RelProofByRel[node.Rel] = model.CgroupRelProof{
			Device: entry.Identity.Device,
			Inode:  entry.Identity.Inode,
			CPUSet: proof.Clone(),
		}
		switch node.Domain {
		case topology.DomainPrimary:
			applied.NonReclaimPool = applied.NonReclaimPool.Union(proof)
		case topology.DomainReclaim:
			applied.ReclaimEffective = applied.ReclaimEffective.Union(proof)
		}
		if node.Role != topology.TopoNodeRoleReclaimNUMABucket {
			continue
		}
		if !node.Constraint.CPUUpperBound.IsEmpty() && !proof.IsSubsetOf(node.Constraint.CPUUpperBound) {
			return nil, fmt.Errorf(
				"reclaim NUMA bucket %q target proof %s exceeds CPU upper bound %s",
				node.Rel, proof.String(), node.Constraint.CPUUpperBound.String(),
			)
		}
		numaID, err := strconv.Atoi(node.Metadata["numa"])
		if err != nil {
			return nil, fmt.Errorf("reclaim NUMA bucket %q has invalid numa metadata %q", node.Rel, node.Metadata["numa"])
		}
		applied.ReclaimEffectivePerNUMA[numaID] = applied.ReclaimEffectivePerNUMA[numaID].Union(proof)
	}
	validatedProofs, err := validateContainerLifecycleProofs(ctx, metaServer, lifecycleProofs)
	if err != nil {
		return nil, err
	}
	// Resolved leaves are not DAG nodes, but their stable final-snapshot identity
	// is the historical authority needed to prove a later same-owner handoff.
	// Reuse the already captured snapshot after strict lifecycle validation; this
	// adds no cgroup reads and does not authorize pending or retired containers.
	for _, proof := range validatedProofs.OrderedProofs {
		if proof.State != containerLifecycleResolved || proof.RelativePath == "" {
			continue
		}
		entry, ok := snapshot.Entries[proof.RelativePath]
		if !ok {
			return nil, fmt.Errorf("final snapshot misses resolved container rel %q", proof.RelativePath)
		}
		applied.RelProofByRel[proof.RelativePath] = model.CgroupRelProof{
			Device: entry.Identity.Device,
			Inode:  entry.Identity.Inode,
			CPUSet: entry.CPUs.Clone(),
		}
	}
	containerCPUSetByPod, err := containerCPUSetByPodFromFinalSnapshot(
		snapshot, validatedProofs, deferredCleanupRels)
	if err != nil {
		return nil, err
	}
	applied.ContainerCPUSetByPod = containerCPUSetByPod
	applied.PoolProjection = buildAppliedPoolProjection(model.AppliedViewLevelFull, desired, applied)
	return applied, nil
}

// CPUSetAdjustmentDisabledHandler implements AdjustmentCapable. It is inert on a
// cgroup v2 host without the opt-in; otherwise it clears the pending-protection
// state and runs resetCPUSetTopology. The reset converges the plugin's controlled
// non-leaf DAG nodes and writes no AppliedView: while preserve mode is on it
// excludes the configured reclaim roots, which ReconcileDisabled aligns, and while
// preserve is off the reset DAG includes them. Converged controlled targets with
// pending-Pod subtrees or post-snapshot descendants still deferred return
// disabledResetNotConvergedError, whose Unwrap classifies ErrDisabledResetDeferred
// for a later retry. UnclassifiedSkipped alone does not defer reset completion;
// physical read/write failures remain hard errors.
func (p *CPUSetTopologyPlugin) CPUSetAdjustmentDisabledHandler(ctx context.Context, in bulkheadapi.HandlerContext) error {
	if p.disabledOnCgroupV2(ctx) {
		return nil
	}
	p.pendingProtections = map[string]pendingPodProtection{}
	budget := topology.NewAdjustmentBudget(
		ctx, topologyBudgetFromConfig(p.cfg.TopologyConvergenceBudget))
	return runCPUSetTopologyAdjustment(ctx, in, budget, p.resetCPUSetTopology)
}

// NeedsDisabledReset implements bulkheadapi.DisabledTopologyReconciler: it reports
// true until the authoritative disabled reset has run once. At process start and
// after a restart resetComplete is false, so the first disabled round performs one
// idempotent reset, even if the targets already have the desired values. A genuine
// enabled->disabled exit and a preserve-mode toggle both clear resetComplete so a
// fresh reset runs.
//
// The safety guarantee no longer depends on an in-memory ownership witness: reset
// restores controlled DAG targets and positively classified safe-to-widen
// intermediate directories. Protected container leaves and unclassified
// descendants are not written; reset does not repair historical leaf drift.
func (p *CPUSetTopologyPlugin) NeedsDisabledReset() bool {
	return !p.resetComplete
}

// MarkDisabledResetComplete records a successful disabled reset; the manager
// calls this inside a generation-fence commit.
func (p *CPUSetTopologyPlugin) MarkDisabledResetComplete() { p.resetComplete = true }

// ReconcileDisabled implements DisabledTopologyReconciler. It returns a None
// outcome when ShouldReconcileWhenDisabled declines the round or DesiredView is
// nil; otherwise it converges only the configured reclaim roots into a ReclaimOnly
// AppliedView and routes the result through toOutcome with requireReclaimOnly, so
// the manager accepts it only on full reclaim-only convergence. It owns the
// reclaim-only view; the disabled reset handler owns the disabled-transition
// reset, excluding the reclaim roots only when preservation is enabled.
func (p *CPUSetTopologyPlugin) ReconcileDisabled(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
) (bulkheadapi.TopologyOutcome, error) {
	if !p.ShouldReconcileWhenDisabled(ctx, in) || in.DesiredView == nil {
		return bulkheadapi.TopologyOutcome{Level: bulkheadapi.ConvergenceLevelNone}, nil
	}
	budget := topology.NewAdjustmentBudget(
		ctx, topologyBudgetFromConfig(p.cfg.TopologyConvergenceBudget))
	var finalAttempt topology.DAGApplyResult
	cumulative, err := runCPUSetTopologyAdjustmentWithResult(
		ctx,
		in,
		budget,
		func(
			ctx context.Context,
			attemptInput bulkheadapi.HandlerContext,
			adjustmentBudget *topology.AdjustmentBudget,
		) (topology.ConvergenceResult, error) {
			attemptResult, convergence, attemptErr := p.reconcileDisabledOnce(ctx, attemptInput, adjustmentBudget)
			finalAttempt = attemptResult
			return convergence, attemptErr
		},
	)
	defer func() {
		emitBulkheadTopologySummary(in.Emitter, "reclaim_only", cumulative, err)
	}()
	result := dagApplyResultFromConvergence(cumulative)
	if err == nil {
		result.AppliedView = finalAttempt.AppliedView.DeepCopy()
	}
	return toOutcome(result, outcomeControl{requireReclaimOnly: true}), err
}

func disabledReconcileShouldRetry(err error, convergence topology.ConvergenceResult) bool {
	return err != nil && convergence.ReplanDisposition.AllowsReplan()
}

func accumulateConvergenceResult(total *topology.ConvergenceResult, attempt topology.ConvergenceResult) {
	if total == nil {
		return
	}
	total.Attempted += attempt.Attempted
	total.Applied += attempt.Applied
	total.Skipped += attempt.Skipped
	total.Failed += attempt.Failed
	total.Deferred += attempt.Deferred
	total.Journal = append(total.Journal, attempt.Journal...)
	total.Rounds = append(total.Rounds, attempt.Rounds...)
	total.Converged = attempt.Converged
	total.ParentSafe = attempt.ParentSafe
	total.State = attempt.State
	total.ConvergenceReport = cloneConvergenceReport(attempt.ConvergenceReport)
	total.FinalSnapshot = topology.CloneCompleteSnapshot(attempt.FinalSnapshot)
	total.FinalSnapshotCurrent = attempt.FinalSnapshotCurrent
	total.DeferredLeafCount = attempt.DeferredLeafCount
	total.DeferredCPUCount = attempt.DeferredCPUCount
	total.ReplanDisposition = attempt.ReplanDisposition
	total.Published = attempt.Published
}

func cloneConvergenceReport(in topology.ConvergenceReport) topology.ConvergenceReport {
	out := in
	if in.NonConvergedTargets != nil {
		out.NonConvergedTargets = make([]topology.RelConvergence, len(in.NonConvergedTargets))
		for i, target := range in.NonConvergedTargets {
			target.Observed = model.CloneOptionalCPUSet(target.Observed)
			target.Target = model.CloneOptionalCPUSet(target.Target)
			out.NonConvergedTargets[i] = target
		}
	}
	out.PendingToPrimary = model.CloneOptionalCPUSet(in.PendingToPrimary)
	out.PendingToReclaim = model.CloneOptionalCPUSet(in.PendingToReclaim)
	out.CleanupPendingPrimary = model.CloneOptionalCPUSet(in.CleanupPendingPrimary)
	out.CleanupPendingReclaim = model.CloneOptionalCPUSet(in.CleanupPendingReclaim)
	return out
}

func (p *CPUSetTopologyPlugin) reconcileDisabledOnce(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
	adjustmentBudget *topology.AdjustmentBudget,
) (topology.DAGApplyResult, topology.ConvergenceResult, error) {
	configured := p.configuredReclaimRels(in.DesiredView, in.Topology)
	observed, err := topology.ObserveConfiguredRels(ctx, p.cgroup, configured)
	if err != nil {
		return topology.DAGApplyResult{}, topology.ConvergenceResult{}, fmt.Errorf("classify reclaim-only rels: %w", err)
	}
	specs, activeRoots, err := p.buildReclaimOnlySpecs(ctx, in, observed)
	if err != nil {
		return topology.DAGApplyResult{}, topology.ConvergenceResult{}, err
	}
	if len(specs) == 0 {
		current, err := topology.ObserveConfiguredRels(ctx, p.cgroup, configured)
		if err != nil {
			return topology.DAGApplyResult{}, topology.ConvergenceResult{}, fmt.Errorf("recheck absent reclaim-only rels: %w", err)
		}
		if !equalRelObservations(observed, current) {
			return topology.DAGApplyResult{}, topology.ConvergenceResult{
				ReplanDisposition: topology.ReplanSafeNoPhysicalWrites,
			}, errReclaimClassificationChanged
		}
		convergence := topology.ConvergenceResult{
			Converged:            true,
			State:                topology.ConvergenceStateConverged,
			FinalSnapshotCurrent: true,
		}
		return topology.DAGApplyResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          reclaimOnlyAppliedView(in.DesiredView, nil, nil),
		}, convergence, nil
	}

	dag, err := topology.BuildDAG(specs)
	if err != nil {
		return topology.DAGApplyResult{}, topology.ConvergenceResult{}, fmt.Errorf("build reclaim-only topology dag: %w", err)
	}
	expectedRes, err := p.buildExpectedCPUSetByRel(ctx, in)
	if err != nil {
		return topology.DAGApplyResult{}, topology.ConvergenceResult{}, fmt.Errorf("build reclaim-only expected container cpuset: %w", err)
	}
	expected := filterCPUSetByRoots(expectedRes.ExpectedByRel, activeRoots)
	deferred := filterCPUSetByRoots(expectedRes.DeferredLeafByRel, activeRoots)
	protected := filterCPUSetByRoots(p.pendingProtectedCPUSetByRel(ctx, expectedRes.PendingByPod), activeRoots)
	absentBoundaries := make(map[string]struct{})
	requiredIdentities := make(map[string]topology.CgroupIdentity)
	activeRels := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		activeRels[spec.Rel] = struct{}{}
	}
	for rel, observation := range observed {
		if !observation.Exists {
			absentBoundaries[rel] = struct{}{}
			continue
		}
		if _, ok := activeRels[rel]; ok {
			requiredIdentities[rel] = observation.Identity
		}
	}

	var cpuDetails machine.CPUDetails
	if in.Topology != nil {
		cpuDetails = in.Topology.CPUDetails
	}
	var finalAppliedView *model.AppliedView
	res, err := (topology.TopologyCoordinator{}).Converge(ctx, topology.CoordinatorInput{
		DAG:                   dag,
		Cgroup:                p.cgroup,
		Mode:                  topology.NormalModeGuardWithGate(p.sharedModeGate()),
		Budget:                adjustmentBudget.RemainingConvergenceBudget(),
		AdjustmentBudget:      adjustmentBudget,
		InitialSnapshot:       adjustmentBudget.InitialSnapshot(),
		DrainSelection:        topologyDrainSelectionFromConfig(p.cfg.TopologyDrainSelection),
		CPUDetails:            cpuDetails,
		ReservedCPUSet:        in.DesiredView.Reserve,
		ExpectedCPUSetByRel:   expected,
		DeferredCPUSetByRel:   deferred,
		ProtectedCPUSetByRel:  protected,
		TraversalBoundaries:   absentBoundaries,
		RequiredIdentityByRel: requiredIdentities,
		ExpectedAbsentRels:    absentBoundaries,
		Objective:             topology.ConvergenceObjectiveFull,
		PublishFinalSnapshot: func(snapshot *topology.CompleteSnapshot) error {
			current, err := topology.ObserveConfiguredRels(ctx, p.cgroup, configured)
			if err != nil {
				return err
			}
			if !equalRelObservations(observed, current) {
				return &topology.PlanStaleError{
					Rel:       "reclaim",
					Direction: topology.WritePublish,
					Resource:  "reclaim_classification",
					Current:   "changed",
					Target:    "preflight_observation",
					Err:       errReclaimClassificationChanged,
				}
			}
			for rel, required := range requiredIdentities {
				entry, ok := snapshot.Entries[rel]
				if !ok || entry.Identity != required {
					return &topology.PlanStaleError{
						Rel:       rel,
						Direction: topology.WritePublish,
						Resource:  "reclaim_identity",
						Current:   fmt.Sprintf("%v", entry.Identity),
						Target:    fmt.Sprintf("%v", required),
						Err:       errReclaimClassificationChanged,
					}
				}
			}
			finalAppliedView = reclaimOnlyAppliedView(in.DesiredView, dag, snapshot)
			if finalAppliedView == nil {
				return fmt.Errorf("derive reclaim-only applied view")
			}
			return nil
		},
	})
	if err != nil {
		return dagApplyResultFromConvergence(res), res, fmt.Errorf("apply reclaim-only topology dag: %w", err)
	}
	result := dagApplyResultFromConvergence(res)
	result.AppliedView = finalAppliedView.DeepCopy()
	if result.FullyConverged && result.FinalSnapshotCurrent && result.AppliedView == nil {
		return topology.DAGApplyResult{}, res, fmt.Errorf("reclaim-only convergence is missing final-snapshot applied view")
	}
	return result, res, nil
}

func (p *CPUSetTopologyPlugin) configuredReclaimRels(
	desired *model.DesiredView,
	cpuTopology *machine.CPUTopology,
) []string {
	rels := make([]string, 0, len(p.cfg.BulkheadReclaimRelPaths))
	numaIDs := map[int]struct{}{}
	if desired != nil {
		for numaID := range desired.ReclaimEffectivePerNUMA {
			numaIDs[numaID] = struct{}{}
		}
	}
	if cpuTopology != nil {
		for _, numaID := range cpuTopology.CPUDetails.NUMANodes().ToSliceInt() {
			numaIDs[numaID] = struct{}{}
		}
	}
	sortedNUMAIDs := make([]int, 0, len(numaIDs))
	for numaID := range numaIDs {
		sortedNUMAIDs = append(sortedNUMAIDs, numaID)
	}
	sort.Ints(sortedNUMAIDs)
	for reclaimIndex, root := range p.cfg.BulkheadReclaimRelPaths {
		root = strings.Trim(root, "/")
		if root == "" {
			continue
		}
		rels = append(rels, root)
		for _, numaID := range sortedNUMAIDs {
			if rel := p.cfg.ReclaimPerNUMA(reclaimIndex, numaID); rel != "" {
				rels = append(rels, strings.Trim(rel, "/"))
			}
		}
	}
	sort.Strings(rels)
	return rels
}

func (p *CPUSetTopologyPlugin) buildReclaimOnlySpecs(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
	observed map[string]topology.RelObservation,
) ([]topology.NodeSpec, []string, error) {
	var cpuDetails machine.CPUDetails
	if in.Topology != nil {
		cpuDetails = in.Topology.CPUDetails
	}
	all, err := bulkheadutils.BuildTopologyNodeSpecsFromView(
		p.cfg, desiredCPUSetPartitionView(in.DesiredView), cpuDetails, nil, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build reclaim-only topology inputs: %w", err)
	}
	activeRootSet := make(map[string]struct{}, len(p.cfg.BulkheadReclaimRelPaths))
	rootParent := make(map[string]string, len(p.cfg.BulkheadReclaimRelPaths))
	for _, spec := range all {
		if spec.Role != topology.TopoNodeRoleReclaim {
			continue
		}
		rel := strings.Trim(spec.Rel, "/")
		if observed[rel].Exists {
			activeRootSet[rel] = struct{}{}
			rootParent[rel] = strings.Trim(spec.ParentRel, "/")
		} else if rel != "" {
			general.InfofV(4, "cpuset_topology: reclaim-only rel path does not exist, skipping, rel=%q", rel)
		}
	}
	for changed := true; changed; {
		changed = false
		for root := range activeRootSet {
			parent := rootParent[root]
			if parent == "" {
				continue
			}
			if _, ok := activeRootSet[parent]; ok {
				continue
			}
			delete(activeRootSet, root)
			changed = true
		}
	}
	activeRoots := make([]string, 0, len(activeRootSet))
	for root := range activeRootSet {
		activeRoots = append(activeRoots, root)
	}
	sort.Strings(activeRoots)
	specs := make([]topology.NodeSpec, 0, len(all))
	for _, spec := range all {
		rel := strings.Trim(spec.Rel, "/")
		switch spec.Role {
		case topology.TopoNodeRoleReclaim:
			if _, ok := activeRootSet[rel]; !ok {
				continue
			}
		case topology.TopoNodeRoleReclaimNUMABucket:
			if !observed[rel].Exists || !withinAnyRelRoot(rel, activeRoots) {
				if rel != "" && !observed[rel].Exists {
					general.InfofV(4, "cpuset_topology: reclaim-only rel path does not exist, skipping, rel=%q", rel)
				}
				continue
			}
			if spec.ParentRel != "" {
				if _, ok := activeRootSet[spec.ParentRel]; !ok {
					continue
				}
			}
		default:
			continue
		}
		spec.Rel = rel
		specs = append(specs, spec)
	}
	// Under preserve, the disabled/reclaim-only reconcile must not grow the
	// running (checkpoint-pinned) reclaim buckets toward a same-cycle Desired
	// expansion. Pin each reclaim NUMA bucket to its current cgroup cpuset and
	// derive the reclaim root envelope from the pinned buckets.
	if p.cfg.PreserveReclaimCPUSetWhenTopologyDisabled {
		if err := p.pinReclaimSpecsToRunning(ctx, specs, cpuDetails); err != nil {
			return nil, nil, err
		}
	}
	return specs, activeRoots, nil
}

// pinReclaimSpecsToRunning keeps the disabled-preserve reconcile from expanding
// reclaim NUMA buckets (or their reclaim root) beyond the cpuset currently
// pinned in cgroup. After a restart this cycle's DesiredView may carry a fresh,
// not-yet-committed reclaim expansion (e.g. 16 CPUs/NUMA while the checkpoint
// kept 2 CPUs/NUMA); the running/checkpoint-approved bucket must win until the
// config actually commits the target (topology re-enables and the normal apply
// path runs).
//
// We only let reclaim shrink toward Desired when Desired is already a subset of
// the running bucket AND the shrink keeps every live SMT pair whole (it does not
// drop one thread of a core while keeping the other); any growth, NUMA
// reassignment, equivalent-but-different whole-core pair, or half-core tear pins
// the bucket to its current cpuset. The reclaim root is recomputed as the union
// of the pinned NUMA buckets so its envelope never out-grows the buckets it
// covers.
func (p *CPUSetTopologyPlugin) pinReclaimSpecsToRunning(
	ctx context.Context,
	specs []topology.NodeSpec,
	cpuDetails machine.CPUDetails,
) error {
	pinnedByRel := make(map[string]machine.CPUSet, len(specs))
	for i := range specs {
		if specs[i].Role != topology.TopoNodeRoleReclaimNUMABucket {
			continue
		}
		desired := specs[i].CPUs
		current, err := p.cgroup.ReadCPUSet(ctx, specs[i].Rel)
		if err != nil {
			// Preserve mode: the running/checkpoint-approved bucket cannot be
			// observed. Do NOT fabricate an identity from the fresh DesiredView
			// (which may carry an equivalent-but-different pair); fail closed so
			// the caller retries once the checkpoint has restored the live cgroup.
			return fmt.Errorf(
				"cpuset_topology: preserve pin cannot read reclaim bucket rel=%q: %w",
				specs[i].Rel, err,
			)
		}
		if current.IsEmpty() {
			// The bucket cgroup exists but holds no cpuset yet (freshly recreated
			// before the checkpoint restored it). Falling back to desired would
			// write the drifted equivalent pair over the preserved identity; fail
			// closed rather than fabricate.
			return fmt.Errorf(
				"cpuset_topology: preserve pin sees empty live reclaim bucket rel=%q, refusing desired %s",
				specs[i].Rel, desired.String(),
			)
		}
		pinned := desired
		switch {
		case !desired.IsSubsetOf(current):
			// Growth, NUMA reassignment, or an equivalent-but-different whole-core
			// pair on the same NUMA: the running/checkpoint-approved bucket wins.
			pinned = current
		case shrinksWholeCorePair(current, desired, cpuDetails):
			// A subset shrink that would tear a live SMT pair (keep one thread of a
			// core, drop its sibling) is not a legal preserve shrink; keep the whole
			// live pair instead of writing a half-core bucket.
			pinned = current
		default:
			// Whole-core-aligned shrink toward desired: pinned already equals desired.
		}
		specs[i].CPUs = pinned
		pinnedByRel[specs[i].Rel] = pinned
	}
	for i := range specs {
		if specs[i].Role != topology.TopoNodeRoleReclaim {
			continue
		}
		union := machine.NewCPUSet()
		for j := range specs {
			if specs[j].Role != topology.TopoNodeRoleReclaimNUMABucket {
				continue
			}
			if specs[j].ParentRel != "" &&
				strings.Trim(specs[j].ParentRel, "/") == strings.Trim(specs[i].Rel, "/") {
				union = union.Union(pinnedByRel[specs[j].Rel])
			}
		}
		if !union.IsEmpty() {
			specs[i].CPUs = union
		}
	}
	return nil
}

// shrinksWholeCorePair reports whether a subset shrink (desired ⊆ current) would
// tear a live SMT pair. For every physical core that has more than one thread
// pinned in the live bucket, desired must keep either all of that core's threads
// or none of them; keeping a strict subset leaves a half-core reclaim bucket,
// which is not a legal preserve shrink. Cores with a single live thread (no SMT
// partner present) are never a tear.
func shrinksWholeCorePair(current, desired machine.CPUSet, details machine.CPUDetails) bool {
	byCore := make(map[int][]int)
	for _, cpu := range current.ToSliceInt() {
		info, ok := details[cpu]
		if !ok {
			continue
		}
		byCore[info.CoreID] = append(byCore[info.CoreID], cpu)
	}
	for _, threads := range byCore {
		// Only a genuine 2-thread SMT core can be torn: dropping one thread of a
		// 2-thread pair leaves a half core. A core with a single live thread (no
		// SMT partner present) or a dummy grouping (more than two threads on one
		// CoreID, e.g. unit-test topology) is never a half-core tear.
		if len(threads) != 2 {
			continue
		}
		kept := 0
		for _, cpu := range threads {
			if desired.Contains(cpu) {
				kept++
			}
		}
		if kept > 0 && kept < len(threads) {
			return true
		}
	}
	return false
}

func reclaimOnlyAppliedView(
	desired *model.DesiredView,
	dag *topology.TopoDAG,
	snapshot *topology.CompleteSnapshot,
) *model.AppliedView {
	if desired == nil {
		return nil
	}
	partition := *desired.CPUSetPartitionView.DeepCopy()
	partition.TransientProtectedNonReclaim = machine.NewCPUSet()
	partition.TransientProtectedNonReclaimPerNUMA = map[int]machine.CPUSet{}
	partition.NonReclaimPool = machine.NewCPUSet()
	partition.ReclaimEffective = machine.NewCPUSet()
	partition.ReclaimEffectivePerNUMA = map[int]machine.CPUSet{}
	partition.ContainerCPUSetByPod = map[string]map[string]machine.CPUSet{}
	applied := &model.AppliedView{
		CPUSetPartitionView: partition,
		Level:               model.AppliedViewLevelReclaimOnly,
		CPUSetByRel:         map[string]machine.CPUSet{},
		RelProofByRel:       map[string]model.CgroupRelProof{},
		PoolProjection:      model.NewAppliedPoolProjection(),
	}
	if dag == nil || snapshot == nil {
		applied.PoolProjection = buildAppliedPoolProjection(model.AppliedViewLevelReclaimOnly, desired, applied)
		return applied
	}
	for _, node := range dag.Nodes() {
		proof, ok := snapshot.TargetProofCPUs(node.Rel, node.CPUs)
		if !ok {
			return nil
		}
		entry := snapshot.Entries[node.Rel]
		applied.CPUSetByRel[node.Rel] = proof.Clone()
		applied.RelProofByRel[node.Rel] = model.CgroupRelProof{
			Device: entry.Identity.Device,
			Inode:  entry.Identity.Inode,
			CPUSet: proof.Clone(),
		}
		if node.Role == topology.TopoNodeRoleReclaim {
			applied.ReclaimEffective = applied.ReclaimEffective.Union(proof)
		}
		if node.Role == topology.TopoNodeRoleReclaimNUMABucket {
			numaID, err := strconv.Atoi(node.Metadata["numa"])
			if err != nil {
				return nil
			}
			applied.ReclaimEffectivePerNUMA[numaID] = applied.ReclaimEffectivePerNUMA[numaID].Union(proof)
		}
	}
	applied.PoolProjection = buildAppliedPoolProjection(model.AppliedViewLevelReclaimOnly, desired, applied)
	return applied
}

func filterCPUSetByRoots(in map[string]machine.CPUSet, roots []string) map[string]machine.CPUSet {
	out := make(map[string]machine.CPUSet)
	for rel, cpus := range in {
		if withinAnyRelRoot(rel, roots) {
			out[rel] = cpus.Clone()
		}
	}
	return out
}

func withinAnyRelRoot(rel string, roots []string) bool {
	rel = strings.Trim(rel, "/")
	for _, root := range roots {
		root = strings.Trim(root, "/")
		if rel == root || strings.HasPrefix(rel, root+"/") {
			return true
		}
	}
	return false
}

func equalRelObservations(a, b map[string]topology.RelObservation) bool {
	if len(a) != len(b) {
		return false
	}
	for rel, left := range a {
		if right, ok := b[rel]; !ok || left != right {
			return false
		}
	}
	return true
}

func (p *CPUSetTopologyPlugin) disabledResetCPUSet(ctx context.Context, in bulkheadapi.HandlerContext) (machine.CPUSet, error) {
	if p.cgroup.Version(ctx) == cgroupclient.CgroupVersionV2 {
		return machine.NewCPUSet(), nil
	}
	if in.Topology == nil {
		return machine.CPUSet{}, fmt.Errorf("nil topology for v1 disabled cpuset reset")
	}
	target := in.Topology.CPUDetails.CPUs()
	if target.IsEmpty() {
		return machine.CPUSet{}, fmt.Errorf("empty machine cpuset for v1 disabled cpuset reset")
	}
	return target, nil
}

func (p *CPUSetTopologyPlugin) buildDisabledResetDAG(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
	target machine.CPUSet,
) (*topology.TopoDAG, error) {
	relExists := func(rel string) error {
		_, err := p.cgroup.StatDir(ctx, rel)
		return err
	}

	discoveredSiblings, err := p.discoverBulkheadReclaimSiblings(ctx, in.DesiredView)
	if err != nil {
		return nil, fmt.Errorf("discover bulkhead reclaim siblings: %w", err)
	}
	configuredSiblingsReset := p.configuredBulkheadReclaimSiblings()
	siblings := p.mergeBulkheadReclaimSiblings(
		discoveredSiblings,
		configuredSiblingsReset,
		desiredCPUSetPartitionView(in.DesiredView),
	)
	general.InfofV(2, "bulkhead: reclaim sibling resolution (disabled-reset) configured=%v discovered=%v merged=%v system_rel=%q",
		configuredSiblingsReset, discoveredSiblings, siblings, p.cfg.BulkheadSystemRelPath)
	if err := p.ensureBulkheadReclaimSiblingDirs(ctx, siblings); err != nil {
		return nil, fmt.Errorf("ensure bulkhead reclaim sibling cgroups: %w", err)
	}

	var cpuDetails machine.CPUDetails
	if in.Topology != nil {
		cpuDetails = in.Topology.CPUDetails
	}
	specs, err := bulkheadutils.BuildTopologyNodeSpecsFromView(p.cfg, desiredCPUSetPartitionView(in.DesiredView), cpuDetails, siblings, relExists)
	if err != nil {
		return nil, fmt.Errorf("build disabled reset topology inputs: %w", err)
	}
	{
		relRoles := make([]string, 0, len(specs))
		for _, sp := range specs {
			relRoles = append(relRoles, sp.Rel+":"+string(sp.Role))
		}
		general.InfofV(2, "bulkhead: topology node specs (disabled-reset) %v", relRoles)
	}
	if p.ShouldReconcileWhenDisabled(ctx, in) {
		reclaimRoots := normalizedReclaimRoots(p.cfg.BulkheadReclaimRelPaths)
		filtered := specs[:0]
		for _, spec := range specs {
			if withinAnyRelRoot(spec.Rel, reclaimRoots) {
				continue
			}
			filtered = append(filtered, spec)
		}
		specs = filtered
	}
	specs, err = p.filterExistingDisabledResetSpecs(ctx, specs)
	if err != nil {
		return nil, err
	}
	if len(specs) == 0 {
		return nil, nil
	}
	for i := range specs {
		specs[i].CPUs = target
		specs[i].Mems = ""
	}

	dag, err := topology.BuildDAG(specs)
	if err != nil {
		return nil, fmt.Errorf("build disabled reset topology dag: %w", err)
	}
	return dag, nil
}

func (p *CPUSetTopologyPlugin) filterExistingDisabledResetSpecs(ctx context.Context, specs []topology.NodeSpec) ([]topology.NodeSpec, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := specs[:0]
	for _, spec := range specs {
		rel := strings.Trim(spec.Rel, "/")
		if rel == "" {
			continue
		}
		if _, err := p.cgroup.StatDir(ctx, rel); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				general.InfofV(4, "cpuset_topology: disabled reset rel path does not exist, skipping, rel=%q err=%v", rel, err)
				continue
			}
			return nil, fmt.Errorf("stat disabled reset rel path %q: %w", rel, err)
		}
		spec.Rel = rel
		out = append(out, spec)
	}
	return out, nil
}

func (p *CPUSetTopologyPlugin) resetCPUSetTopology(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
	adjustmentBudget *topology.AdjustmentBudget,
) (topology.ConvergenceResult, error) {
	target, err := p.disabledResetCPUSet(ctx, in)
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", "reset_target_error")
		return topology.ConvergenceResult{}, err
	}

	// Reset (disabled transition) drives controlled topology-owned nodes, QoS class
	// dirs, and positively-identified fresh Pod dirs back towards the machine/root
	// cpuset. Protected leaves (Expected/Deferred and every fresh pod-status
	// container ID) are NEVER written by the disabled reset — not even to their
	// canonical allocation. A leaf without fresh-pod proof (stale pod, unknown dir)
	// is deferred, not fanned out to the parent target: a mis-widened managed
	// container does NOT self-heal (containerd's enable_update_resource_spec_check
	// short-circuits UpdateContainerResources when the spec is unchanged), so a
	// historical drift in a protected leaf is not repaired by this change. Pending
	// allocations whose leaves cannot be resolved are deferred and retried next round.
	expectedRes, err := p.buildExpectedCPUSetByRel(ctx, in)
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", "container_error")
		return topology.ConvergenceResult{}, fmt.Errorf("build disabled reset container ownership proof: %w", err)
	}
	// buildExpectedCPUSetByRel only fetches the strict fresh snapshot when there are
	// resolvable outcomes. The disabled reset must still positively classify out-of-view
	// (non-QRM) pods on the empty-outcomes path, so backfill the single snapshot here
	// when the build did not. Either way the classification below reuses this one
	// generation and never issues a second GetPodList; a fetch error withholds the reset.
	if expectedRes.FreshPodsByUID == nil {
		freshPodsByUID, err := fetchStrictFreshPodsByUID(ctx, in)
		if err != nil {
			emitBulkheadPruneResult(in.Emitter, "skipped", "fresh_snapshot_error")
			return topology.ConvergenceResult{}, fmt.Errorf("disabled reset fresh pod snapshot: %w", err)
		}
		expectedRes.FreshPodsByUID = freshPodsByUID
	}
	expected := expectedRes.ExpectedByRel

	dag, err := p.buildDisabledResetDAG(ctx, in, target)
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", "dag_error")
		return topology.ConvergenceResult{}, err
	}
	if dag == nil {
		emitBulkheadPruneResult(in.Emitter, "success", "")
		return topology.ConvergenceResult{
			Converged:            true,
			State:                topology.ConvergenceStateConverged,
			FinalSnapshotCurrent: true,
		}, nil
	}

	protectedLeaves := protectedLeafRels(expectedRes)
	safeWiden := map[string]struct{}{}
	// Positive classification reuses the single strict fresh snapshot already fetched by
	// the ownership build (expectedRes.FreshPodsByUID): widen each fresh Pod dir (and its
	// QoS parent) and mark every fresh pod-status container ID (current + lastState) as a
	// protected leaf. No second GetPodList is issued here, so the build and the
	// classification always observe the same pod generation.
	p.classifyFreshPodsForReset(expectedRes.FreshPodsByUID, protectedLeaves, safeWiden)
	// Union the protected-leaf ancestor closure into the fresh-pod safeWiden set,
	// then drop any protected leaf itself (defense in depth).
	for rel := range safeWidenRelsForReset(protectedLeaves) {
		safeWiden[rel] = struct{}{}
	}
	for rel := range protectedLeaves {
		delete(safeWiden, path.Clean(rel))
	}

	var traversalBoundaries map[string]struct{}
	if p.ShouldReconcileWhenDisabled(ctx, in) {
		traversalBoundaries = reclaimResetBoundaries(p.cfg, in.DesiredView)
	}
	res, err := (topology.TopologyCoordinator{}).Converge(ctx, topology.CoordinatorInput{
		DAG:                 dag,
		Cgroup:              p.cgroup,
		Mode:                topology.ResetModeGuardWithGate(p.sharedModeGate()),
		ExpectedCPUSetByRel: expected,
		ProtectedLeafByRel:  protectedLeaves,
		PendingPodDirs:      p.pendingPodDirRels(expectedRes),
		SafeWidenRels:       safeWiden,
		TraversalBoundaries: traversalBoundaries,
		Budget:              adjustmentBudget.RemainingConvergenceBudget(),
		AdjustmentBudget:    adjustmentBudget,
		InitialSnapshot:     adjustmentBudget.InitialSnapshot(),
		DrainSelection:      topologyDrainSelectionFromConfig(p.cfg.TopologyDrainSelection),
	})
	if err != nil {
		emitBulkheadPruneResult(in.Emitter, "skipped", "dag_error")
		emitBulkheadTopologySummary(in.Emitter, "reset", res, err)
		return res, fmt.Errorf("apply disabled reset topology dag: %w", err)
	}
	emitBulkheadTopologySummary(in.Emitter, "reset", res, nil)
	if !res.Converged {
		general.InfofV(4, "cpuset_topology: disabled reset not fully converged, report=%+v", res.ConvergenceReport)
		emitBulkheadPruneResult(in.Emitter, "skipped", "reset_not_converged")
		return res, &disabledResetNotConvergedError{
			state:    res.State,
			applied:  res.Applied,
			deferred: res.Deferred,
			failed:   res.Failed,
			report:   res.ConvergenceReport,
		}
	}

	emitBulkheadPruneResult(in.Emitter, "success", "")
	return res, nil
}

func normalizedReclaimRoots(rels []string) []string {
	out := make([]string, 0, len(rels))
	for _, rel := range rels {
		if rel = strings.Trim(rel, "/"); rel != "" {
			out = append(out, rel)
		}
	}
	return out
}

func reclaimResetBoundaries(
	cfg bulkheadconfig.BulkheadConfiguration,
	desired *model.DesiredView,
) map[string]struct{} {
	if !cfg.PreserveReclaimCPUSetWhenTopologyDisabled {
		return nil
	}
	out := make(map[string]struct{})
	for reclaimIndex, root := range cfg.BulkheadReclaimRelPaths {
		if root = strings.Trim(root, "/"); root == "" {
			continue
		}
		out[root] = struct{}{}
		if desired == nil {
			continue
		}
		for numaID := range desired.ReclaimEffectivePerNUMA {
			if rel := cfg.ReclaimPerNUMA(reclaimIndex, numaID); rel != "" {
				out[strings.Trim(rel, "/")] = struct{}{}
			}
		}
	}
	return out
}

func (p *CPUSetTopologyPlugin) sharedModeGate() *topology.ModeGate {
	p.modeGateMu.Lock()
	defer p.modeGateMu.Unlock()
	if p.modeGate == nil {
		p.modeGate = topology.NewModeGate()
	}
	return p.modeGate
}

func (p *CPUSetTopologyPlugin) PeriodicalHandler(
	ctx context.Context,
	in bulkheadapi.PeriodicalHandlerContext,
) error {
	if p.disabledOnCgroupV2(ctx) {
		return nil
	}
	enabled := enableBulkheadCpusetTopologyByDynamicConf(in.DynamicConf)
	if in.EffectiveEnabled != nil {
		enabled = *in.EffectiveEnabled
	}
	if enabled && in.EffectiveEnabled != nil && in.AppliedView == nil {
		return nil
	}
	if p.cgroup.Version(ctx) == cgroupclient.CgroupVersionV1 {
		schedLoadBalance := !enabled
		if err := p.cgroup.ApplySchedLoadBalance(ctx, "", schedLoadBalance); err != nil {
			return fmt.Errorf("apply root cpuset.sched_load_balance=%t: %w", schedLoadBalance, err)
		}
		return nil
	}

	flag := cgcommon.CPUSetPartitionFlagMember
	if enabled {
		flag = cgcommon.CPUSetPartitionFlagRoot
	}
	return p.applyBulkheadPartitionFlag(ctx, flag)
}

func (p *CPUSetTopologyPlugin) applyBulkheadPartitionFlag(ctx context.Context, flag cgcommon.CPUSetPartitionFlag) error {
	var errs []error
	for _, rel := range p.cfg.BulkheadPartitionRelPaths {
		rel = strings.Trim(rel, "/")
		if rel == "" {
			continue
		}
		if _, err := p.cgroup.StatDir(ctx, rel); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("stat partition rel path %q: %w", rel, err))
				continue
			}
			general.InfofV(4, "bulkhead: partition rel path does not exist, skipping, rel=%q err=%v", rel, err)
			continue
		}
		if err := p.cgroup.ApplyCPUSetPartition(ctx, rel, flag); err != nil {
			if errors.Is(err, cgcommon.ErrNotSupported) {
				general.InfofV(4, "bulkhead: cpuset partition not supported, skipping, rel=%q", rel)
				continue
			}
			errs = append(errs, fmt.Errorf("apply cpuset.cpus.partition=%s @ %s: %w", flag, rel, err))
			continue
		}
	}
	return apierrors.NewAggregate(errs)
}

// pendingContainerCPUSet records a container whose allocation already exists in
// QRM state but whose cgroup rel cannot be resolved yet (typically the pod
// admit window before kubelet/containerd creates the container). With no leaf
// available to write, its cpuset protects the controlled ancestors' effective
// targets from shrinking below the allocation; the absent leaf is not written.
type pendingContainerCPUSet struct {
	PodUID         string
	ContainerName  string
	ContainerID    string
	CPUs           machine.CPUSet
	Reason         string
	Cause          error
	NativeQOSClass v1.PodQOSClass
	ScopeRel       string
	// sharedNonReclaimProjection marks THIS container's CPUs as eligible for the
	// shared non-reclaim projection. It mirrors topology.PendingProtection and is
	// resolved from the transient prepare hint (derived from the specific
	// AllocationInfo) at the pending choke point; never from native QoS,
	// RequestQuantity, or a live config read.
	sharedNonReclaimProjection bool
	// projectionCPUs is the per-container subset of CPUs that is projected. It is
	// filled from CPUs only for containers flagged above, so a mixed pod (one
	// eligible shared ramp-up container plus an exact sibling) projects only the
	// eligible container's CPUs and never contaminates the exact sibling.
	projectionCPUs machine.CPUSet
}

type resolvedContainerCPUSet struct {
	PodUID        string
	ContainerName string
	ContainerID   string
	Rel           string
	CPUs          machine.CPUSet
}

type podContainerCPUSetOutcomes struct {
	resolved []resolvedContainerCPUSet
	pending  []pendingContainerCPUSet
}

type podLifecycleClassification struct {
	Resolved []resolvedContainerCPUSet
	Pending  []pendingContainerCPUSet
	Retired  []containerLifecycleProof
	ScopeRel string
	QOSClass v1.PodQOSClass
	Stale    bool
}

// expectedCPUSetBuildResult separates resolvable container leaves (ExpectedByRel,
// written precisely) from admit-pending containers (PendingByPod, protected but
// not written).
type expectedCPUSetBuildResult struct {
	ExpectedByRel     map[string]machine.CPUSet
	CurrentByRel      map[string]machine.CPUSet
	DeferredLeafByRel map[string]machine.CPUSet
	PendingByPod      []pendingContainerCPUSet
	// FreshPodsByUID is the single strict fresh pod snapshot obtained for the whole
	// reset round (UID keyed). The ownership build sets it on the resolvable-outcomes
	// path; the disabled reset entry backfills it on the empty-outcomes path so fresh
	// classification still covers out-of-view (non-QRM) pods. Classification always
	// consumes this one generation and never issues a second GetPodList.
	FreshPodsByUID  map[string]*v1.Pod
	LifecycleProofs containerLifecycleProofSet
}

// protectedLeafRels returns the set of protected leaf cgroup rels the disabled
// reset must leave untouched: every container the fresh build resolved (both
// QRM-allocated leaves and deferred/pending leaves). The disabled reset skips these
// rather than resetting them to the parent target; kubelet owns protected leaves.
func protectedLeafRels(res *expectedCPUSetBuildResult) map[string]struct{} {
	out := map[string]struct{}{}
	if res == nil {
		return out
	}
	for rel := range res.ExpectedByRel {
		out[rel] = struct{}{}
	}
	for rel := range res.DeferredLeafByRel {
		out[rel] = struct{}{}
	}
	// Also include every fresh pod-status container rel the lifecycle proofs resolved
	// (containerStatuses / initContainerStatuses / ephemeralContainerStatuses,
	// current and lastState) — non-QRM protected leaves must never be considered
	// for widening, even when QRM has no allocation for them.
	for _, proof := range res.LifecycleProofs.OrderedProofs {
		if rel := strings.Trim(proof.RelativePath, "/"); rel != "" {
			out[path.Clean(rel)] = struct{}{}
		}
	}
	return out
}

// pendingPodDirRels resolves the Pod-level cgroup dirs that contain an admit-pending
// container. The disabled reset expands these Pod dirs but writes none of their
// subdirs and reports not converged, so MarkDisabledResetComplete is held back and
// the next round retries.
func (p *CPUSetTopologyPlugin) pendingPodDirRels(res *expectedCPUSetBuildResult) map[string]struct{} {
	out := map[string]struct{}{}
	if res == nil {
		return out
	}
	for _, pending := range res.PendingByPod {
		for _, rel := range p.pendingPodScopeCandidatesForQOS(pending.PodUID, pending.NativeQOSClass) {
			if rel = strings.Trim(rel, "/"); rel != "" {
				out[path.Clean(rel)] = struct{}{}
			}
		}
	}
	return out
}

// safeWidenRelsForReset derives the non-controlled rels the disabled reset may widen
// from the positively-identified protected leaves: the parent Pod dir of every resolved
// protected leaf, and that Pod dir's QoS class parent. A Pod dir is considered
// positively identified only when a fresh container (Expected/Deferred, or a fresh
// status container ID) resolves under it — an unknown dir under a QoS class has no
// such proof and stays deferred. Historically drifted protected leaves are NOT
// restored by this fix; they are left untouched (see protectedLeafRels).
// classifyFreshPodsForReset positively classifies the pods in the single strict
// fresh snapshot shared with the ownership build
// (expectedCPUSetBuildResult.FreshPodsByUID): each fresh Pod dir (QoS + configured
// cgroup path candidates) and its QoS parent are added to safeWiden; every fresh
// pod-status container ID (containerStatuses / init / ephemeral, current and
// lastState.Terminated) is resolved to a rel and added to protectedLeaves. Long-lived
// non-QRM pods are thus classified deterministically and do not pin the reset
// non-converged. The snapshot is reused as-is: this must not issue a second
// GetPodList, which could observe a divergent pod generation mid-round. A nil
// snapshot (the early-return paths with no resolvable outcome) leaves both sets
// unchanged; the strict fetch itself, and its error, live in buildExpectedCPUSetByRel
// so a fetch failure withholds the whole reset before classification runs.
func (p *CPUSetTopologyPlugin) classifyFreshPodsForReset(
	freshPodsByUID map[string]*v1.Pod,
	protectedLeaves map[string]struct{},
	safeWiden map[string]struct{},
) {
	addProtectedLeaf := func(podUID, containerID string) {
		containerID = native.TrimContainerIDPrefix(containerID)
		if containerID == "" {
			return
		}
		rel, err := p.resolveContainerRelSafe(podUID, containerID)
		if err != nil || rel == "" {
			general.InfofV(2, "cpuset_topology: disabled reset could not resolve container id=%q rel: %v", containerID, err)
			return
		}
		protectedLeaves[path.Clean(rel)] = struct{}{}
	}
	for uid, pod := range freshPodsByUID {
		if pod == nil {
			continue
		}
		for _, rel := range p.pendingPodScopeCandidatesForQOS(uid, pod.Status.QOSClass) {
			if rel = strings.Trim(rel, "/"); rel != "" {
				safeWiden[path.Clean(rel)] = struct{}{}
				if qos := path.Dir(rel); qos != "." && qos != "/" && qos != rel {
					safeWiden[path.Clean(qos)] = struct{}{}
				}
			}
		}
		for _, cs := range pod.Status.ContainerStatuses {
			addProtectedLeaf(uid, cs.ContainerID)
			if cs.LastTerminationState.Terminated != nil {
				addProtectedLeaf(uid, cs.LastTerminationState.Terminated.ContainerID)
			}
		}
		for _, cs := range pod.Status.InitContainerStatuses {
			addProtectedLeaf(uid, cs.ContainerID)
			if cs.LastTerminationState.Terminated != nil {
				addProtectedLeaf(uid, cs.LastTerminationState.Terminated.ContainerID)
			}
		}
		for _, cs := range pod.Status.EphemeralContainerStatuses {
			addProtectedLeaf(uid, cs.ContainerID)
			if cs.LastTerminationState.Terminated != nil {
				addProtectedLeaf(uid, cs.LastTerminationState.Terminated.ContainerID)
			}
		}
	}
}

func safeWidenRelsForReset(protectedLeaves map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for rel := range protectedLeaves {
		podDir := path.Dir(rel)
		if podDir == "." || podDir == "/" {
			continue
		}
		out[path.Clean(podDir)] = struct{}{}
		if qos := path.Dir(podDir); qos != "." && qos != "/" && qos != podDir {
			out[path.Clean(qos)] = struct{}{}
		}
	}
	// Defense in depth: a protected leaf must never itself be widened, even if the
	// dirname closure or a future edit accidentally lands a protected-leaf rel in the set.
	for rel := range protectedLeaves {
		delete(out, path.Clean(rel))
	}
	return out
}

type expectedCPUSetOwnerBinder struct {
	ownerByRel map[string]expectedCPUSetOwner
}

// expectedCPUSetOwner persists the logical owner and the consensus CPUSet recorded
// for a relative path. The consensus must not be read back from whichever target
// map (ExpectedByRel or DeferredLeafByRel) is being filled on the current call.
type expectedCPUSetOwner struct {
	key  containerLifecycleProofKey
	cpus machine.CPUSet
}

func (b *expectedCPUSetOwnerBinder) bind(
	target map[string]machine.CPUSet,
	container resolvedContainerCPUSet,
) error {
	owner := lifecycleProofLogicalKey(container.PodUID, container.ContainerName)
	if previous, exists := b.ownerByRel[container.Rel]; exists &&
		(previous.key.podUID != owner.podUID || !previous.cpus.Equals(container.CPUs)) {
		return fmt.Errorf(
			"relative path %q is owned by both pod=%q container=%q and pod=%q container=%q",
			container.Rel, previous.key.podUID, previous.key.containerName, owner.podUID, owner.containerName)
	}
	b.ownerByRel[container.Rel] = expectedCPUSetOwner{key: owner, cpus: container.CPUs}
	target[container.Rel] = container.CPUs
	return nil
}

// PendingCPUSetUnion returns the union of all pending container allocations. The
// writer folds this into the primary effective target so the parent cgroup never
// shrinks below an allocation whose leaf has not been created yet.
func (r *expectedCPUSetBuildResult) PendingCPUSetUnion() machine.CPUSet {
	out := machine.NewCPUSet()
	if r == nil {
		return out
	}
	for _, p := range r.PendingByPod {
		out = out.Union(p.CPUs)
	}
	return out
}

// isContainerAbsentErr reports whether the pod or container has no active
// runtime leaf during the normal admission creation or restart window.
func isContainerAbsentErr(err error) bool {
	return metapod.IsPodNotFound(err) ||
		errors.Is(err, metapod.ErrContainerNotFound) ||
		errors.Is(err, bulkheadutils.ErrContainerNotRunning)
}

// isContainerPendingErr additionally treats a confirmed identity change as an
// admission-safe transition. Cache synchronization, kubelet transport, and
// context errors must fail closed.
func isContainerPendingErr(err error) bool {
	if isContainerAbsentErr(err) || errors.Is(err, bulkheadutils.ErrContainerIdentityChanged) {
		return true
	}
	var resolveErr *bulkheadutils.ContainerRelPathResolveError
	return errors.As(err, &resolveErr) &&
		resolveErr.Stage == bulkheadutils.ContainerRelPathResolveStageCgroupPath &&
		errors.Is(resolveErr.Err, os.ErrNotExist)
}

// fetchStrictFreshPodsByUID obtains the single strict (bypassed) fresh pod snapshot
// for a whole reset round, UID keyed. It is the one and only GetPodList the disabled
// reset issues: the container-ownership build and the fresh-pod classification both
// derive from this same generation, so classification must never trigger a second,
// possibly divergent fetch. A nil MetaServer or a kubelet error fails closed so the
// reset is withheld rather than acting on a stale or partial view.
func fetchStrictFreshPodsByUID(ctx context.Context, in bulkheadapi.HandlerContext) (map[string]*v1.Pod, error) {
	if in.MetaServer == nil {
		return nil, fmt.Errorf("metaserver unavailable for strict fresh pod snapshot")
	}
	refreshCtx := context.WithValue(ctx, metapod.BypassCacheKey, metapod.BypassCacheTrue)
	refreshCtx = context.WithValue(refreshCtx, metapod.StrictBypassCacheKey, metapod.BypassCacheTrue)
	freshPods, err := in.MetaServer.GetPodList(refreshCtx, nil)
	if err != nil {
		return nil, fmt.Errorf("get strict fresh pod snapshot: %w", err)
	}
	freshPodsByUID := make(map[string]*v1.Pod, len(freshPods))
	for _, pod := range freshPods {
		if pod == nil {
			return nil, fmt.Errorf("strict fresh pod snapshot contains nil pod")
		}
		podUID := string(pod.UID)
		if podUID == "" {
			return nil, fmt.Errorf("strict fresh pod snapshot contains pod with empty UID")
		}
		if _, exists := freshPodsByUID[podUID]; exists {
			return nil, fmt.Errorf("strict fresh pod snapshot contains duplicate UID %q", podUID)
		}
		freshPodsByUID[podUID] = pod
	}
	return freshPodsByUID, nil
}

// buildExpectedCPUSetByRel uses a two-snapshot protocol. It first resolves
// candidate leaves from one cache-only Pod snapshot without triggering kubelet
// I/O, then obtains exactly one strict fresh Pod snapshot for the whole round.
// Every stale, pending, and generation decision is derived from that same fresh
// snapshot; per-container refreshes are forbidden because they could mix Pod
// generations inside one topology plan.
func (p *CPUSetTopologyPlugin) buildExpectedCPUSetByRel(ctx context.Context, in bulkheadapi.HandlerContext) (*expectedCPUSetBuildResult, error) {
	out := &expectedCPUSetBuildResult{
		ExpectedByRel:     map[string]machine.CPUSet{},
		CurrentByRel:      map[string]machine.CPUSet{},
		DeferredLeafByRel: map[string]machine.CPUSet{},
	}
	if in.DesiredView == nil || !hasNonEmptyDesiredCPUSet(in.DesiredView.ContainerCPUSetByPod) {
		var err error
		out.LifecycleProofs, err = freezeContainerLifecycleProofs(nil, nil)
		if err != nil {
			return nil, fmt.Errorf("freeze empty container lifecycle proofs: %w", err)
		}
		return out, nil
	}
	if in.MetaServer == nil {
		return nil, fmt.Errorf("build container lifecycle proofs: metaserver is nil")
	}
	var errs []error
	cachedPodsByUID, cacheOnly, err := cachedPodSnapshotByUID(ctx, in.MetaServer)
	if err != nil {
		return nil, fmt.Errorf("get cache-only pod snapshot: %w", err)
	}
	outcomesByPod := make(map[string]*podContainerCPUSetOutcomes)
	for podUID, containers := range in.DesiredView.ContainerCPUSetByPod {
		outcomes := &podContainerCPUSetOutcomes{}
		outcomesByPod[podUID] = outcomes
		for containerName, cpus := range containers {
			if cpus.IsEmpty() {
				continue
			}
			// Reuse ResolveContainerRelPath so that the rel-key format stays in sync
			// with everywhere else in bulkhead (BulkheadPrimaryRelPath,
			// BulkheadReclaimRelPaths, CollectActiveRels, controlledRels, and the
			// childRel constructed by writer.TopologyCoordinatorConverge via filepath.Join(parent, name)).
			// ResolveContainerRelPath does the GetContainerID + GetContainerRelativeCgroupPath
			// lookup and, crucially, trims the leading "/" that
			// GetKubernetesAnyExistRelativeCgroupPath prepends. Without this trim, the
			// expected map key would never match the childRel that expandDescendants
			// produces during recursion, causing per-container cpuset enforcement to
			// silently degrade to inheriting the parent pool target.
			var rel, containerID string
			var err error
			if cacheOnly {
				containerID, _ = podStatusContainerID(cachedPodsByUID[podUID], containerName)
				if containerID == "" {
					err = &bulkheadutils.ContainerRelPathResolveError{
						Stage: bulkheadutils.ContainerRelPathResolveStageContainerID,
						Err:   metapod.ErrContainerNotFound,
					}
				} else {
					rel, err = p.resolveContainerRelSafe(podUID, containerID)
				}
			} else {
				rel, containerID, err = bulkheadutils.ResolveContainerRelPathAndIDCacheOnlyWithContext(
					ctx, in.MetaServer, podUID, containerName)
			}
			if err != nil {
				if isContainerPendingErr(err) {
					outcomes.pending = append(outcomes.pending, pendingContainerCPUSet{
						PodUID: podUID, ContainerName: containerName, ContainerID: containerID,
						CPUs: cpus, Reason: err.Error(), Cause: err,
					})
					continue
				}
				// A real internal error (illegal rel, cgroup/metaserver failure, or a
				// refresh/transport failure): block this round rather than apply a
				// partial/wrong topology.
				errs = append(errs, fmt.Errorf("pod=%s container=%s cpuset=%s: %w",
					podUID, containerName, cpus.String(), err))
				continue
			}
			if rel == "" {
				errs = append(errs, fmt.Errorf("pod=%s container=%s cpuset=%s: empty relative cgroup path",
					podUID, containerName, cpus.String()))
				continue
			}
			outcomes.resolved = append(outcomes.resolved, resolvedContainerCPUSet{
				PodUID: podUID, ContainerName: containerName, ContainerID: containerID, Rel: rel, CPUs: cpus,
			})
		}
	}
	hasOutcomes := false
	for _, outcomes := range outcomesByPod {
		if len(outcomes.resolved) > 0 || len(outcomes.pending) > 0 {
			hasOutcomes = true
			break
		}
	}
	if !hasOutcomes {
		if len(errs) > 0 {
			return nil, apierrors.NewAggregate(errs)
		}
		out.LifecycleProofs, err = freezeContainerLifecycleProofs(nil, nil)
		if err != nil {
			return nil, fmt.Errorf("freeze empty container lifecycle proofs: %w", err)
		}
		return out, nil
	}
	freshPodsByUID, err := fetchStrictFreshPodsByUID(ctx, in)
	if err != nil {
		return nil, err
	}
	// Reuse this single strict snapshot for both the ownership build and the
	// disabled-reset fresh-pod classification below; classification must not issue a
	// second GetPodList (which could observe a divergent pod generation mid-round).
	out.FreshPodsByUID = freshPodsByUID
	var lifecycleProofs []containerLifecycleProof
	ownerBinder := expectedCPUSetOwnerBinder{
		ownerByRel: make(map[string]expectedCPUSetOwner),
	}
	for podUID, outcomes := range outcomesByPod {
		if len(outcomes.resolved) == 0 && len(outcomes.pending) == 0 {
			continue
		}
		freshPod, exists := freshPodsByUID[podUID]
		classification, scopeErr := p.filterPodOutcomesAgainstFreshPod(
			ctx, podUID, outcomes, freshPod, exists)
		if scopeErr != nil {
			errs = append(errs, fmt.Errorf("resolve pod outcomes: pod=%s: %w", podUID, scopeErr))
			continue
		}
		if classification.Stale {
			for _, container := range outcomes.resolved {
				general.Infof("bulkhead: stale checkpoint allocation skipped from expected leaves, pod=%q container=%q cpuset=%s",
					podUID, container.ContainerName, container.CPUs.String())
			}
			for _, container := range outcomes.pending {
				general.Infof("bulkhead: stale checkpoint allocation skipped from pending protection, pod=%q container=%q cpuset=%s",
					podUID, container.ContainerName, container.CPUs.String())
			}
		}
		podProofs := lifecycleProofsForDesiredPod(
			podUID, in.DesiredView.ContainerCPUSetByPod[podUID], freshPod, exists, classification)
		lifecycleProofs = append(lifecycleProofs, podProofs...)
		if classification.Stale {
			continue
		}
		for _, container := range classification.Resolved {
			if in.Mode.OrFullDefault() == cpusetutil.CPUSetAdjustmentModeAdmission {
				current, readErr := p.cgroup.ReadCPUSet(ctx, container.Rel)
				if readErr == nil {
					out.CurrentByRel[container.Rel] = current.Clone()
				}
				if readErr == nil && !current.Equals(container.CPUs) {
					// Symmetric reclaim-carve case: an already-placed protected leaf
					// is currently narrower than its desired allocation, and the
					// only CPUs it is missing are exactly the reclaim-effective
					// set. Reclaim has already been carved out of this leaf, so
					// its enforced cpuset is the correct reclaim-free physical
					// expectation. Demanding that the leaf grow back into reclaim
					// is unsafe and the planner correctly refuses it; binding the
					// stale full allocation as the expected target instead makes
					// the convergence proof see a permanent
					// dynamic_target_mismatch, which deadlocks the fixed-point
					// compiler with a bare ErrNoProgress. Bind the already-correct
					// reclaim-free cpuset as the expected target so convergence
					// holds without any write.
					if current.IsSubsetOf(container.CPUs) &&
						container.CPUs.Difference(current).IsSubsetOf(in.DesiredView.DesiredReclaimEffective) &&
						current.Intersection(in.DesiredView.DesiredReclaimEffective).IsEmpty() {
						carved := container
						carved.CPUs = current.Clone()
						if bindErr := ownerBinder.bind(out.ExpectedByRel, carved); bindErr != nil {
							errs = append(errs, bindErr)
						}
						continue
					}
				}
			}
			if bindErr := ownerBinder.bind(out.ExpectedByRel, container); bindErr != nil {
				errs = append(errs, bindErr)
			}
		}
		for i := range classification.Pending {
			classification.Pending[i].NativeQOSClass = classification.QOSClass
			classification.Pending[i].ScopeRel = classification.ScopeRel
			// Resolve the shared non-reclaim projection eligibility per container
			// from the transient prepare-snapshot hint (derived from the specific
			// AllocationInfo under the non-overlap (overlap=false) admission gate)
			// at the single pending choke point, so both the cache-pending and the
			// fresh-generation-pending paths inherit it identically. The lookup is
			// by exact (podUID, containerName); it is never derived from native
			// QoS, RequestQuantity, a live config read, or OR-ed across siblings.
			classification.Pending[i].sharedNonReclaimProjection = isPendingSharedProjectionEligible(
				in.PendingSharedProjection, podUID, classification.Pending[i].ContainerName)
			if classification.Pending[i].sharedNonReclaimProjection {
				// This specific container's CPUs are the projected subset; the rest
				// of the pod's pending containers stay exact.
				classification.Pending[i].projectionCPUs = classification.Pending[i].CPUs.Clone()
			}
			general.InfofV(5, "bulkhead: container rel pending, protecting allocation, pod=%q container=%q cpuset=%s cpuset_size=%d reason=%s",
				podUID, classification.Pending[i].ContainerName, classification.Pending[i].CPUs.String(), classification.Pending[i].CPUs.Size(), classification.Pending[i].Reason)
		}
		out.PendingByPod = append(out.PendingByPod, classification.Pending...)
	}
	if coverageErr := validateLifecycleProofCoverage(
		in.DesiredView.ContainerCPUSetByPod, lifecycleProofs); coverageErr != nil {
		errs = append(errs, fmt.Errorf("validate container lifecycle proof coverage: %w", coverageErr))
	}
	if len(errs) > 0 {
		return nil, apierrors.NewAggregate(errs)
	}
	out.LifecycleProofs, err = freezeContainerLifecycleProofs(lifecycleProofs, freshPodsByUID)
	if err != nil {
		return nil, fmt.Errorf("freeze container lifecycle proofs: %w", err)
	}
	return out, nil
}

func hasNonEmptyDesiredCPUSet(desiredByPod map[string]map[string]machine.CPUSet) bool {
	for _, desiredByContainer := range desiredByPod {
		for _, cpus := range desiredByContainer {
			if !cpus.IsEmpty() {
				return true
			}
		}
	}
	return false
}

// isPendingSharedProjectionEligible reports whether the pending allocation for
// (podUID, containerName) was classified at prepare time as an eligible non-SNB
// shared ramp-up under the non-overlap (overlap=false) admission gate. The
// lookup is a pure per-container membership test against the transient prepare
// hint; it never reads native QoS, RequestQuantity, or live dynamic configuration.
func isPendingSharedProjectionEligible(projection map[string]map[string]struct{}, podUID, containerName string) bool {
	if len(projection) == 0 {
		return false
	}
	containers, ok := projection[podUID]
	if !ok {
		return false
	}
	_, ok = containers[containerName]
	return ok
}

// projectionCPUsOrNil returns a clone of the projected subset, or the zero (nil)
// CPUSet when no container on the pending protection is eligible. Keeping the zero
// value (rather than an empty non-nil set) preserves the "fully exact" signal the
// shadow uses to split exact vs projected.
func projectionCPUsOrNil(cs machine.CPUSet) machine.CPUSet {
	if cs.IsEmpty() {
		return machine.CPUSet{}
	}
	return cs.Clone()
}

// cachedPodSnapshotByUID uses the optional cache-only extension when available.
// Legacy fetchers may fall back to per-container cache reads, but every outcome
// is still reconciled against one strict snapshot before it can affect a plan.
// Cache snapshot errors are fatal and never select the legacy path silently.
func cachedPodSnapshotByUID(
	ctx context.Context,
	metaServer *metaserver.MetaServer,
) (map[string]*v1.Pod, bool, error) {
	if metaServer == nil || metaServer.MetaAgent == nil || metaServer.PodFetcher == nil {
		return nil, false, fmt.Errorf("nil pod fetcher")
	}
	fetcher, ok := metaServer.PodFetcher.(metapod.CachedPodSnapshotFetcher)
	if !ok {
		return nil, false, nil
	}
	pods, err := fetcher.GetPodListFromCache(ctx, nil)
	if err != nil {
		return nil, true, err
	}
	byUID := make(map[string]*v1.Pod, len(pods))
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		byUID[string(pod.UID)] = pod
	}
	return byUID, true, nil
}

func resolveContainerRelPathFromID(podUID, containerID string) (string, error) {
	rel, err := cgcommon.GetContainerRelativeCgroupPath(podUID, containerID)
	if err != nil {
		return "", &bulkheadutils.ContainerRelPathResolveError{
			Stage: bulkheadutils.ContainerRelPathResolveStageCgroupPath,
			Err:   err,
		}
	}
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return "", &bulkheadutils.ContainerRelPathResolveError{
			Stage: bulkheadutils.ContainerRelPathResolveStageCgroupPath,
			Err:   fmt.Errorf("empty relative cgroup path"),
		}
	}
	return rel, nil
}

// filterPodOutcomesAgainstFreshPod applies one round-wide strict Pod snapshot
// to all resolved and pending outcomes for one Pod.
func (p *CPUSetTopologyPlugin) filterPodOutcomesAgainstFreshPod(
	ctx context.Context,
	podUID string,
	outcomes *podContainerCPUSetOutcomes,
	pod *v1.Pod,
	podExists bool,
) (podLifecycleClassification, error) {
	var classification podLifecycleClassification
	validResolved, resolvedPending, retiredResolved, err := p.reconcileResolvedContainersWithFreshPod(
		ctx, podUID, outcomes.resolved, pod)
	if err != nil {
		return classification, err
	}
	pendingResolved, validPending, retiredPending, err := p.reconcilePendingContainersWithFreshPod(
		ctx, podUID, outcomes.pending, pod)
	if err != nil {
		return classification, err
	}
	classification.Resolved = append(validResolved, pendingResolved...)
	classification.Pending = append(validPending, resolvedPending...)
	classification.Retired = append(retiredResolved, retiredPending...)
	if podExists {
		classification.QOSClass = v1qos.GetPodQOS(pod)
		if len(classification.Pending) == 0 {
			return classification, nil
		}
		candidates := p.pendingPodScopeCandidatesForQOS(podUID, classification.QOSClass)
		if len(candidates) == 1 {
			classification.ScopeRel = strings.Trim(candidates[0], "/")
			return classification, nil
		}
		// Prefer a uniquely materialized scope and fail closed if multiple
		// candidates exist. No materialized candidate is still a live pending
		// Pod; the topology DAG selects its controlled primary scope later.
		scopeRel, _, err := p.selectConcretePendingPodScope(ctx, podUID, candidates)
		classification.ScopeRel = scopeRel
		return classification, err
	}
	if len(classification.Pending) == 0 {
		classification.Stale = len(classification.Resolved) == 0
		return classification, nil
	}
	scopeRel, stale, err := p.selectConcretePendingPodScope(
		ctx, podUID, relativePendingPodScopeCandidates(
			cgcommon.GetPodRelativeCgroupPathCandidates(podUID)))
	classification.ScopeRel = scopeRel
	classification.Stale = stale && len(classification.Resolved) == 0
	if classification.Stale {
		for _, pending := range classification.Pending {
			classification.Retired = append(classification.Retired,
				lifecycleProofFromPending(pending, containerLifecycleRetired))
		}
		classification.Pending = nil
	}
	return classification, err
}

// reconcileResolvedContainersWithFreshPod treats container identity as
// generation-scoped. A fresh status may name a new generation while the
// previously resolved cgroup still exists and can still hold tasks. The old
// generation remains protected until typed absence proves its physical
// retirement, while the fresh generation is independently resolved or kept
// pending. Any non-absence stat or path error aborts the round.
func (p *CPUSetTopologyPlugin) reconcileResolvedContainersWithFreshPod(
	ctx context.Context,
	podUID string,
	resolved []resolvedContainerCPUSet,
	pod *v1.Pod,
) ([]resolvedContainerCPUSet, []pendingContainerCPUSet, []containerLifecycleProof, error) {
	valid := make([]resolvedContainerCPUSet, 0, len(resolved)*2)
	var pending []pendingContainerCPUSet
	var retired []containerLifecycleProof
	for _, container := range resolved {
		freshName := pod != nil && podSpecHasContainerName(pod, container.ContainerName)
		freshContainerID, hasFreshID := podStatusContainerID(pod, container.ContainerName)
		sameGeneration := freshName && hasFreshID && freshContainerID == container.ContainerID
		if !sameGeneration {
			if p.cgroup == nil {
				return nil, nil, nil, fmt.Errorf("stat previously resolved container rel %q: cgroup client is nil", container.Rel)
			}
			if _, statErr := p.cgroup.StatDir(ctx, container.Rel); statErr == nil {
				valid = append(valid, container)
			} else if !errors.Is(statErr, os.ErrNotExist) {
				return nil, nil, nil, fmt.Errorf("stat previously resolved container rel %q: %w", container.Rel, statErr)
			} else if !freshName {
				retired = append(retired, lifecycleProofFromResolved(container, containerLifecycleRetired))
			}
		}

		if !freshName {
			continue
		}
		if !hasFreshID {
			pending = append(pending, pendingFromResolved(container, "",
				"fresh pod status has no current container identity"))
			continue
		}
		fresh, resolveErr := p.resolveMaterializedContainerFromFreshID(
			ctx, podUID, container, freshContainerID)
		if resolveErr == nil {
			valid = append(valid, fresh)
			continue
		}
		if errors.Is(resolveErr, os.ErrNotExist) {
			pending = append(pending, pendingFromResolved(container, freshContainerID,
				fmt.Sprintf("fresh container identity %q has no cgroup leaf", freshContainerID)))
			continue
		}
		return nil, nil, nil, fmt.Errorf(
			"resolve fresh container generation pod=%q container=%q id=%q: %w",
			podUID, container.ContainerName, freshContainerID, resolveErr)
	}
	return valid, pending, retired, nil
}

// resolveMaterializedContainerFromFreshID does not trust a successful path
// lookup alone: resolver caches may outlive the cgroup leaf they identify.
func (p *CPUSetTopologyPlugin) resolveMaterializedContainerFromFreshID(
	ctx context.Context,
	podUID string,
	previous resolvedContainerCPUSet,
	containerID string,
) (resolvedContainerCPUSet, error) {
	rel, err := p.resolveContainerRelSafe(podUID, containerID)
	if err != nil {
		return resolvedContainerCPUSet{}, err
	}
	if p.cgroup == nil {
		return resolvedContainerCPUSet{}, fmt.Errorf("stat fresh container rel %q: cgroup client is nil", rel)
	}
	if _, err := p.cgroup.StatDir(ctx, rel); err != nil {
		return resolvedContainerCPUSet{}, err
	}
	return resolvedContainerCPUSet{
		PodUID:        podUID,
		ContainerName: previous.ContainerName,
		ContainerID:   containerID,
		Rel:           rel,
		CPUs:          previous.CPUs,
	}, nil
}

func pendingFromResolved(
	container resolvedContainerCPUSet,
	containerID, reason string,
) pendingContainerCPUSet {
	return pendingContainerCPUSet{
		PodUID:        container.PodUID,
		ContainerName: container.ContainerName,
		ContainerID:   containerID,
		CPUs:          container.CPUs,
		Reason:        reason,
		Cause:         os.ErrNotExist,
	}
}

func lifecycleProofFromResolved(
	container resolvedContainerCPUSet,
	state containerLifecycleState,
) containerLifecycleProof {
	return containerLifecycleProof{
		PodUID:        container.PodUID,
		ContainerName: container.ContainerName,
		ContainerID:   container.ContainerID,
		RelativePath:  container.Rel,
		DesiredCPUSet: container.CPUs,
		State:         state,
	}
}

func lifecycleProofFromPending(
	container pendingContainerCPUSet,
	state containerLifecycleState,
) containerLifecycleProof {
	return containerLifecycleProof{
		PodUID:        container.PodUID,
		ContainerName: container.ContainerName,
		ContainerID:   container.ContainerID,
		DesiredCPUSet: container.CPUs,
		State:         state,
	}
}

// lifecycleProofsForDesiredPod freezes exactly one lifecycle decision for every
// non-empty desired entry. The strict fresh Pod snapshot alone determines proof
// state: an absent Pod or owner is retired, an owner without a current runtime
// identity is pending, and an owner with a current runtime identity is resolved.
// Physical classification may enrich the proof with the matching rel or an old
// retired identity, but it must never change that snapshot-owned state.
func lifecycleProofsForDesiredPod(
	podUID string,
	desired map[string]machine.CPUSet,
	freshPod *v1.Pod,
	podExists bool,
	classification podLifecycleClassification,
) []containerLifecycleProof {
	proofs := make([]containerLifecycleProof, 0, len(desired))
	for containerName, cpus := range desired {
		if cpus.IsEmpty() {
			continue
		}
		ownerExists := podExists && podSpecHasContainerName(freshPod, containerName)
		freshID, hasFreshID := podStatusContainerID(freshPod, containerName)
		proof := containerLifecycleProof{
			PodUID:        podUID,
			ContainerName: containerName,
			ContainerID:   freshID,
			DesiredCPUSet: cpus,
			State:         containerLifecycleRetired,
		}
		if ownerExists {
			proof.State = containerLifecyclePending
			if hasFreshID {
				if resolved, ok := findResolvedLifecycleContainer(
					classification.Resolved, containerName, freshID); ok && resolved.Rel != "" {
					proof.State = containerLifecycleResolved
					proof.RelativePath = resolved.Rel
				}
			}
		} else {
			if retired, ok := findRetiredLifecycleContainer(
				classification.Retired, containerName); ok {
				proof.ContainerID = retired.ContainerID
				proof.RelativePath = retired.RelativePath
			} else if resolved, ok := findResolvedLifecycleContainer(
				classification.Resolved, containerName, ""); ok {
				proof.ContainerID = resolved.ContainerID
				proof.RelativePath = resolved.Rel
			}
		}
		proofs = append(proofs, proof)
	}
	return proofs
}

func findResolvedLifecycleContainer(
	containers []resolvedContainerCPUSet,
	name, containerID string,
) (resolvedContainerCPUSet, bool) {
	for _, container := range containers {
		if container.ContainerName == name &&
			(containerID == "" || container.ContainerID == containerID) {
			return container, true
		}
	}
	return resolvedContainerCPUSet{}, false
}

func findRetiredLifecycleContainer(
	proofs []containerLifecycleProof,
	name string,
) (containerLifecycleProof, bool) {
	for _, proof := range proofs {
		if proof.ContainerName == name {
			return proof, true
		}
	}
	return containerLifecycleProof{}, false
}

func podStatusContainerID(pod *v1.Pod, name string) (string, bool) {
	if pod == nil {
		return "", false
	}
	statusGroups := [][]v1.ContainerStatus{
		pod.Status.ContainerStatuses,
		pod.Status.InitContainerStatuses,
		pod.Status.EphemeralContainerStatuses,
	}
	for _, statuses := range statusGroups {
		for _, status := range statuses {
			if status.Name == name {
				containerID := native.TrimContainerIDPrefix(status.ContainerID)
				if containerID != "" {
					return containerID, true
				}
			}
		}
	}
	return "", false
}

// reconcilePendingContainersWithFreshPod treats the round-wide fresh Pod Spec
// as the canonical owner of container names. A pending checkpoint entry whose
// name is absent from that Spec is retired even while the Pod cgroup remains:
// pod-level liveness cannot prove ownership for one container. Entries with a
// current Spec name are rebound only to the fresh status ID; if its cgroup leaf
// is not materialized yet, the allocation remains pending instead of falling
// back to a stale cached identity.
func (p *CPUSetTopologyPlugin) reconcilePendingContainersWithFreshPod(
	ctx context.Context,
	podUID string,
	pending []pendingContainerCPUSet,
	pod *v1.Pod,
) ([]resolvedContainerCPUSet, []pendingContainerCPUSet, []containerLifecycleProof, error) {
	var resolved []resolvedContainerCPUSet
	valid := make([]pendingContainerCPUSet, 0, len(pending))
	var retired []containerLifecycleProof
	for _, container := range pending {
		if pod == nil {
			valid = append(valid, container)
			continue
		}
		if !podSpecHasContainerName(pod, container.ContainerName) {
			retired = append(retired, lifecycleProofFromPending(container, containerLifecycleRetired))
			continue
		}
		containerID, ok := podStatusContainerID(pod, container.ContainerName)
		if !ok {
			valid = append(valid, container)
			continue
		}
		fresh, err := p.resolveMaterializedContainerFromFreshID(ctx, podUID, resolvedContainerCPUSet{
			PodUID:        podUID,
			ContainerName: container.ContainerName,
			CPUs:          container.CPUs,
		}, containerID)
		if err == nil {
			resolved = append(resolved, fresh)
			continue
		}
		if errors.Is(err, os.ErrNotExist) {
			container.ContainerID = containerID
			valid = append(valid, container)
			continue
		}
		return nil, nil, nil, fmt.Errorf(
			"resolve current generation for pending pod=%q container=%q id=%q: %w",
			podUID, container.ContainerName, containerID, err)
	}
	return resolved, valid, retired, nil
}

func podSpecHasContainerName(pod *v1.Pod, name string) bool {
	for _, container := range pod.Spec.Containers {
		if container.Name == name {
			return true
		}
	}
	for _, container := range pod.Spec.InitContainers {
		if container.Name == name {
			return true
		}
	}
	for _, container := range pod.Spec.EphemeralContainers {
		if container.Name == name {
			return true
		}
	}
	return false
}

func relativePendingPodScopeCandidates(candidates []string) []string {
	out := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		out = append(out, strings.Trim(candidate, "/"))
	}
	return out
}

func (p *CPUSetTopologyPlugin) pendingPodScopeCandidatesForQOS(
	podUID string,
	qosClass v1.PodQOSClass,
) []string {
	primary := strings.Trim(p.cfg.BulkheadPrimaryRelPath, "/")
	if primary == "" {
		return relativePendingPodScopeCandidates(
			cgcommon.GetPodRelativeCgroupPathCandidatesForQOS(podUID, qosClass))
	}
	configured := cgcommon.GetPodRelativeCgroupPathCandidatesForQOS(podUID, qosClass)
	underPrimary := make([]string, 0, len(configured))
	for _, candidate := range configured {
		rel := strings.Trim(candidate, "/")
		if rel == primary || strings.HasPrefix(rel, primary+"/") {
			underPrimary = append(underPrimary, rel)
		}
	}
	if len(underPrimary) > 0 {
		return underPrimary
	}

	// Custom primary roots are outside the Kubernetes root registry. Preserve
	// the cgroupfs hierarchy shape relative to that explicitly configured root.
	podName := cgcommon.PodCgroupPathPrefix + podUID
	switch qosClass {
	case v1.PodQOSGuaranteed:
		return []string{path.Join(primary, podName)}
	case v1.PodQOSBurstable:
		return []string{path.Join(primary, "burstable", podName)}
	case v1.PodQOSBestEffort:
		return []string{path.Join(primary, "besteffort", podName)}
	default:
		return cgcommon.GetPodRelativeCgroupPathCandidates(podUID)
	}
}

// selectConcretePendingPodScope observes each normalized, allowed candidate
// once. An all-typed-ENOENT result only authorizes the caller to classify the
// allocation as stale after a fresh Pod lookup also reports that the Pod is
// absent; every other observation error fails closed.
func (p *CPUSetTopologyPlugin) selectConcretePendingPodScope(
	ctx context.Context,
	podUID string,
	candidates []string,
) (string, bool, error) {
	if p.cgroup == nil {
		return "", false, fmt.Errorf("cgroup client is nil")
	}
	allowed := p.allowedPendingPodScopeCandidates(podUID)
	normalized := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		rel, err := normalizePendingPodScopeCandidate(candidate)
		if err != nil {
			return "", false, err
		}
		if _, ok := allowed[rel]; !ok {
			return "", false, fmt.Errorf("pending pod scope candidate %q is outside allowed roots", candidate)
		}
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		normalized = append(normalized, rel)
	}

	var concrete []string
	for _, rel := range normalized {
		if _, err := p.cgroup.StatDir(ctx, rel); err == nil {
			concrete = append(concrete, rel)
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", false, err
		}
	}
	switch len(concrete) {
	case 0:
		return "", true, nil
	case 1:
		return concrete[0], false, nil
	default:
		return "", false, fmt.Errorf("%w: pod=%q candidates=%v",
			errPendingPodScopeAmbiguous, podUID, concrete)
	}
}

func normalizePendingPodScopeCandidate(candidate string) (string, error) {
	if candidate == "" || strings.HasPrefix(candidate, "/") {
		return "", fmt.Errorf("unsafe pending pod scope candidate %q", candidate)
	}
	for _, component := range strings.Split(candidate, "/") {
		if component == "." || component == ".." {
			return "", fmt.Errorf("unsafe pending pod scope candidate %q", candidate)
		}
	}
	rel := path.Clean(candidate)
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("unsafe pending pod scope candidate %q", candidate)
	}
	return rel, nil
}

func (p *CPUSetTopologyPlugin) allowedPendingPodScopeCandidates(podUID string) map[string]struct{} {
	allowed := make(map[string]struct{})
	add := func(candidates []string) {
		for _, candidate := range candidates {
			rel := path.Clean(strings.Trim(candidate, "/"))
			if rel != "" && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
				allowed[rel] = struct{}{}
			}
		}
	}
	add(cgcommon.GetPodRelativeCgroupPathCandidates(podUID))
	add(p.pendingPodScopeCandidatesForQOS(podUID, v1.PodQOSGuaranteed))
	add(p.pendingPodScopeCandidatesForQOS(podUID, v1.PodQOSBurstable))
	add(p.pendingPodScopeCandidatesForQOS(podUID, v1.PodQOSBestEffort))
	return allowed
}

// reclassifyAdmissionDeferredLeaves removes only owner-proven leaf mismatches
// from the synchronous admission objective. Admission performs no leaf writes;
// the existing Full retry converges exact targets through TopologyCoordinator.
func (p *CPUSetTopologyPlugin) reclassifyAdmissionDeferredLeaves(
	ctx context.Context,
	in bulkheadapi.HandlerContext,
	shadowReclaim machine.CPUSet,
	expectedRes *expectedCPUSetBuildResult,
) {
	if expectedRes == nil || len(expectedRes.ExpectedByRel) == 0 ||
		in.State == nil || in.DesiredView == nil {
		return
	}
	proofByRel := make(map[string]containerLifecycleProof, len(expectedRes.LifecycleProofs.OrderedProofs))
	for _, proof := range expectedRes.LifecycleProofs.OrderedProofs {
		if proof.State == containerLifecycleResolved && proof.RelativePath != "" {
			proofByRel[proof.RelativePath] = proof
		}
	}
	for rel, target := range expectedRes.ExpectedByRel {
		current, ok := expectedRes.CurrentByRel[rel]
		var readErr error
		if !ok {
			current, readErr = p.cgroup.ReadCPUSet(ctx, rel)
		}
		if readErr != nil || current.Equals(target) ||
			!current.Intersection(shadowReclaim).IsEmpty() ||
			!target.Intersection(shadowReclaim).IsEmpty() {
			continue
		}
		proof, ok := proofByRel[rel]
		if !ok || !proof.DesiredCPUSet.Equals(target) {
			continue
		}
		allocation := in.State.GetAllocationInfo(proof.PodUID, proof.ContainerName)
		ownerID, ok := bulkheadutils.LeafPoolIdentityForAllocation(allocation)
		if !ok || !ownerID.Valid() {
			continue
		}
		desiredOwner, ok := in.DesiredView.PoolOwners[ownerID]
		if !ok || !target.IsSubsetOf(desiredOwner.ExpectedCPUSet) {
			continue
		}
		applied := in.AppliedView
		if applied == nil {
			continue
		}
		projection := applied.PoolProjection
		historicalOwner, ok := projection.CPUSetByIdentity[ownerID]
		if !ok || !target.IsSubsetOf(historicalOwner) ||
			!target.Intersection(projection.AmbiguousCPUs).IsEmpty() ||
			!target.Intersection(projection.UncoveredCPUs).IsEmpty() {
			continue
		}
		conflict := false
		for otherOwner, cpus := range projection.CPUSetByIdentity {
			if otherOwner != ownerID && !target.Intersection(cpus).IsEmpty() {
				conflict = true
				break
			}
		}
		if conflict {
			continue
		}

		// Growing a strict subset releases no CPU, so fresh lifecycle identity,
		// canonical desired ownership and absence of historical conflicts are
		// sufficient. Superset cleanup and relocation release current-only CPUs;
		// require the latest applied rel proof and same-owner projection to cover
		// the complete current set before allowing unrelated admission to proceed.
		if !current.Difference(target).IsEmpty() {
			relProof, ok := applied.RelProofByRel[rel]
			if !ok || !relProof.CPUSet.Equals(current) {
				continue
			}
			if !current.IsSubsetOf(historicalOwner) {
				continue
			}
		}
		expectedRes.DeferredLeafByRel[rel] = target.Clone()
		delete(expectedRes.ExpectedByRel, rel)
	}
}

func topologyCoversProtectedView(topology *machine.CPUTopology, view *model.DesiredView, protected machine.CPUSet) bool {
	if topology == nil || view == nil || protected.IsEmpty() || view.DesiredReclaimEffective.IsEmpty() || topology.CPUDetails.NUMANodes().Size() == 0 {
		return false
	}
	covered := machine.NewCPUSet()
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		covered = covered.Union(topology.CPUDetails.CPUsInNUMANodes(numaID))
	}
	return protected.IsSubsetOf(covered) && view.DesiredReclaimEffective.IsSubsetOf(covered)
}

func (p *CPUSetTopologyPlugin) pendingProtectedCPUSetByRel(ctx context.Context, pendingByPod []pendingContainerCPUSet) map[string]machine.CPUSet {
	if len(pendingByPod) == 0 {
		p.pendingProtections = map[string]pendingPodProtection{}
		return nil
	}
	if p.now == nil {
		p.now = time.Now
	}
	if p.pendingProtections == nil {
		p.pendingProtections = map[string]pendingPodProtection{}
	}
	now := p.now()
	pendingByPodIndex := make(map[string]int, len(pendingByPod))
	aggregatedPending := make([]pendingContainerCPUSet, 0, len(pendingByPod))
	active := map[string]struct{}{}
	for _, pending := range pendingByPod {
		active[pending.PodUID] = struct{}{}
		if index, ok := pendingByPodIndex[pending.PodUID]; ok {
			aggregatedPending[index].CPUs = aggregatedPending[index].CPUs.Union(pending.CPUs)
			continue
		}
		pendingByPodIndex[pending.PodUID] = len(aggregatedPending)
		aggregatedPending = append(aggregatedPending, pending)
	}
	out := make(map[string]machine.CPUSet, len(aggregatedPending))
	for _, pending := range aggregatedPending {
		protection, ok := p.pendingProtections[pending.PodUID]
		if !ok {
			protection = pendingPodProtection{
				protectUntil: now.Add(defaultPendingPodProtectionTTL),
			}
		} else if !now.Before(protection.protectUntil) {
			expiredAt := protection.protectUntil
			protection.protectUntil = now.Add(defaultPendingPodProtectionTTL)
			general.Warningf("bulkhead: pending pod protection TTL expired while live pending state remains; renewing protection, pod=%q allocation=%s allocation_size=%d expired_at=%s protect_until=%s",
				pending.PodUID, pending.CPUs.String(), pending.CPUs.Size(),
				expiredAt.Format(time.RFC3339Nano), protection.protectUntil.Format(time.RFC3339Nano))
		}
		rel := protection.rel
		if rel == "" {
			var err error
			rel, err = cgcommon.GetPodRelativeCgroupPath(pending.PodUID)
			if err != nil {
				p.pendingProtections[pending.PodUID] = protection
				continue
			}
		}
		rel = strings.Trim(rel, "/")
		if rel == "" {
			p.pendingProtections[pending.PodUID] = protection
			continue
		}
		current, err := p.cgroup.ReadCPUSet(ctx, rel)
		if err != nil || current.IsEmpty() {
			general.InfofV(5, "bulkhead: pending protected rel skipped, pod=%q container=%q rel=%q allocation=%s allocation_size=%d current=%s err=%v reason=missing_or_empty_pod_cgroup protect_until=%s",
				pending.PodUID, pending.ContainerName, rel, pending.CPUs.String(), pending.CPUs.Size(),
				current.String(), err, protection.protectUntil.Format(time.RFC3339Nano))
			p.pendingProtections[pending.PodUID] = protection
			continue
		}
		protection.rel = rel
		protection.current = current
		p.pendingProtections[pending.PodUID] = protection
		// Only protect the pending allocation itself, never the pod cgroup
		// current. The pod cgroup current of a pending pod frequently inherits a
		// much wider cpuset (e.g. the kubepods primary set) before its own
		// allocation is applied. Protecting that wide current would let the
		// transient protected union swallow the reclaim pool and drive
		// ReclaimEffective(PerNUMA) to empty. current is kept for diagnostics
		// only.
		protected := pending.CPUs
		out[rel] = protected
		general.InfofV(5, "bulkhead: pending protected rel, pod=%q container=%q rel=%q allocation=%s allocation_size=%d current=%s protected=%s protected_size=%d overlap=%s dropped_extra=%s protect_until=%s",
			pending.PodUID, pending.ContainerName, rel, pending.CPUs.String(), pending.CPUs.Size(),
			current.String(), protected.String(), protected.Size(),
			current.Intersection(pending.CPUs).String(), current.Difference(pending.CPUs).String(),
			protection.protectUntil.Format(time.RFC3339Nano))
	}
	for podUID := range p.pendingProtections {
		if _, ok := active[podUID]; !ok {
			delete(p.pendingProtections, podUID)
		}
	}
	return out
}

// pendingProtectionScopes owns the single normal-path filesystem read for each
// pending scope. Downstream normal-path consumers must use the returned source
// classification instead of reading the scope again.
// pendingProtectionScopes resolves one stable cgroup observation per scope.
// A successful read proves that the scope exists even when its configured
// cpuset is empty; only typed ENOENT authorizes treating it as not materialized.
// Every other read or stat failure remains fail-closed.
func (p *CPUSetTopologyPlugin) pendingProtectionScopes(
	ctx context.Context,
	dag *topology.TopoDAG,
	pendingByPod []pendingContainerCPUSet,
) ([]topology.PendingProtection, error) {
	if p.now == nil {
		p.now = time.Now
	}
	if p.pendingProtections == nil {
		p.pendingProtections = map[string]pendingPodProtection{}
	}

	now := p.now()
	aggregated := make(map[string]pendingContainerCPUSet, len(pendingByPod))
	active := make(map[string]struct{}, len(pendingByPod))
	for _, pending := range pendingByPod {
		active[pending.PodUID] = struct{}{}
		current, ok := aggregated[pending.PodUID]
		if !ok {
			aggregated[pending.PodUID] = pending
			continue
		}
		current.CPUs = current.CPUs.Union(pending.CPUs)
		if current.ScopeRel == "" {
			current.ScopeRel = pending.ScopeRel
		} else if pending.ScopeRel != "" && current.ScopeRel != pending.ScopeRel {
			return nil, fmt.Errorf("%w: pod=%q scopes=%q,%q",
				errPendingPodScopeAmbiguous, pending.PodUID, current.ScopeRel, pending.ScopeRel)
		}
		if current.NativeQOSClass == "" {
			current.NativeQOSClass = pending.NativeQOSClass
		}
		// Project only the eligible containers' CPUs. A pod may mix an eligible
		// shared ramp-up container with an exact sibling; the exact sibling's CPUs
		// are folded whole-core while the eligible container's CPUs are projected.
		// This is a per-container CPU-subset union, not a pod-wide OR flag. Keep the
		// projected subset nil when no container is eligible so the protection's
		// SharedProjectedCPUs stays the zero (fully-exact) value.
		if !pending.projectionCPUs.IsEmpty() {
			if current.projectionCPUs.IsEmpty() {
				current.projectionCPUs = pending.projectionCPUs.Clone()
			} else {
				current.projectionCPUs = current.projectionCPUs.Union(pending.projectionCPUs)
			}
		}
		aggregated[pending.PodUID] = current
	}

	out := make([]topology.PendingProtection, 0, len(aggregated))
	// This invocation owns one observation per scope. Multiple pending Pod
	// records may resolve to the same scope, so they must share that observation.
	type scopeObservation struct {
		current machine.CPUSet
		source  topology.PendingProtectionSource
		err     error
	}
	observedByRel := make(map[string]scopeObservation)
	for podUID, pending := range aggregated {
		protection, ok := p.pendingProtections[podUID]
		if !ok || !now.Before(protection.protectUntil) {
			protection.protectUntil = now.Add(defaultPendingPodProtectionTTL)
		}
		rel := pending.ScopeRel
		if rel == "" {
			rel = protection.rel
		}
		if rel == "" {
			var err error
			candidates := cgcommon.GetPodRelativeCgroupPathCandidatesForQOS(
				podUID, pending.NativeQOSClass)
			rel, err = dag.SelectUniqueControlledPrimaryCandidate(candidates)
			if err != nil {
				return nil, fmt.Errorf("resolve pending pod scope %q with native qos %q from candidates %v: %w",
					podUID, pending.NativeQOSClass, candidates, err)
			}
		}
		rel = path.Clean(strings.Trim(rel, "/"))
		if rel == "." || rel == "" || rel == ".." || strings.HasPrefix(rel, "../") {
			return nil, fmt.Errorf("%w: pod=%q scope=%q",
				topology.ErrInvalidPendingProtection, podUID, rel)
		}

		observation, observed := observedByRel[rel]
		if !observed {
			current, readErr := p.cgroup.ReadCPUSet(ctx, rel)
			observation.current = current
			switch {
			case readErr == nil:
				observation.source = topology.PendingProtectionSourceExistingPod
			default:
				_, statErr := p.cgroup.StatDir(ctx, rel)
				switch {
				case statErr == nil:
					observation.err = fmt.Errorf("read cpuset for existing pending pod scope %q: %w", rel, readErr)
				case errors.Is(statErr, syscall.ENOENT):
					observation.source = topology.PendingProtectionSourceExpectedPod
				default:
					observation.err = fmt.Errorf("stat pending pod scope %q after cpuset read failed: %w", rel, statErr)
				}
			}
			observedByRel[rel] = observation
		}
		if observation.err != nil {
			return nil, observation.err
		}
		if observation.source == topology.PendingProtectionSourceExistingPod {
			protection.current = observation.current
		}
		protection.rel = rel
		p.pendingProtections[podUID] = protection
		general.Infof("bulkhead: pending pod scope selected, pod=%q native_qos=%q scope=%q source=%q cpuset=%s",
			podUID, pending.NativeQOSClass, rel, observation.source, pending.CPUs.String())
		out = append(out, topology.PendingProtection{
			ScopeRel:            rel,
			CPUs:                pending.CPUs.Clone(),
			PodUID:              podUID,
			Source:              observation.source,
			SharedProjectedCPUs: projectionCPUsOrNil(pending.projectionCPUs),
		})
	}
	for podUID := range p.pendingProtections {
		if _, ok := active[podUID]; !ok {
			delete(p.pendingProtections, podUID)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ScopeRel != out[j].ScopeRel {
			return out[i].ScopeRel < out[j].ScopeRel
		}
		return out[i].PodUID < out[j].PodUID
	})
	return out, nil
}

// pendingProtectedCPUSetByResolvedScopes derives protection only from the
// already resolved scope classifications, avoiding duplicate cgroup I/O.
func pendingProtectedCPUSetByResolvedScopes(protections []topology.PendingProtection) map[string]machine.CPUSet {
	out := make(map[string]machine.CPUSet)
	for _, protection := range protections {
		if protection.Source != topology.PendingProtectionSourceExistingPod {
			continue
		}
		out[protection.ScopeRel] = out[protection.ScopeRel].Union(protection.CPUs)
	}
	return out
}

func unionCPUSetByRel(byRel map[string]machine.CPUSet) machine.CPUSet {
	union := machine.NewCPUSet()
	for _, cpus := range byRel {
		union = union.Union(cpus)
	}
	return union
}

func pendingProtectionCPUSetUnion(protections []topology.PendingProtection) machine.CPUSet {
	union := machine.NewCPUSet()
	for _, protection := range protections {
		union = union.Union(protection.CPUs)
	}
	return union
}

func formatCPUSetByRel(byRel map[string]machine.CPUSet) string {
	if len(byRel) == 0 {
		return "{}"
	}
	rels := make([]string, 0, len(byRel))
	for rel := range byRel {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	parts := make([]string, 0, len(rels))
	for _, rel := range rels {
		parts = append(parts, fmt.Sprintf("%s=%s", rel, byRel[rel].String()))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// reclaimSiblingExcludedSet is the single canonical rel set that must never be
// projected as a reclaim sibling: the primary root, the reclaim roots, the
// partition roots, and the materialized per-NUMA reclaim buckets for the current
// view. Both sibling discovery and merge build from this one helper.
func (p *CPUSetTopologyPlugin) reclaimSiblingExcludedSet(perNUMAReclaim map[int]machine.CPUSet) map[string]struct{} {
	excluded := map[string]struct{}{}
	add := func(rel string) {
		rel = strings.Trim(rel, "/")
		if rel != "" {
			excluded[rel] = struct{}{}
		}
	}
	add(p.cfg.BulkheadPrimaryRelPath)
	for _, rel := range p.cfg.BulkheadReclaimRelPaths {
		add(rel)
	}
	for _, rel := range p.cfg.BulkheadPartitionRelPaths {
		add(rel)
	}
	for reclaimIdx := range p.cfg.BulkheadReclaimRelPaths {
		for numaID := range perNUMAReclaim {
			add(p.cfg.ReclaimPerNUMA(reclaimIdx, numaID))
		}
	}
	return excluded
}

func (p *CPUSetTopologyPlugin) discoverBulkheadReclaimSiblings(ctx context.Context, view *model.DesiredView) ([]string, error) {
	if !p.cfg.EnableBulkheadReclaimSiblings || p.cgroup.Version(ctx) != cgroupclient.CgroupVersionV1 {
		return nil, nil
	}

	var perNUMAReclaim map[int]machine.CPUSet
	if view != nil {
		perNUMAReclaim = view.ReclaimEffectivePerNUMA
	}
	excluded := p.reclaimSiblingExcludedSet(perNUMAReclaim)

	seen := map[string]struct{}{}
	var out []string
	for _, reclaimRel := range p.cfg.BulkheadReclaimRelPaths {
		reclaimRel = strings.Trim(reclaimRel, "/")
		if reclaimRel == "" {
			continue
		}
		parentRel := path.Dir(reclaimRel)
		if parentRel == "." {
			parentRel = ""
		}
		// Ownership condition: auto-discovered reclaim siblings must be cgroups
		// that share a managed parent directory with the reclaim root. When the
		// reclaim root itself sits at the cgroup root (parentRel == ""), its
		// "siblings" are every top-level host/system cgroup (e.g. system, tiger,
		// kubedaemonsets, k8s.io). Those are not reclaim-domain cgroups; scanning
		// them would let cpuset_topology become a second owner that projects
		// ReclaimEffective onto them. During shared ramp-up ReclaimEffective shrinks
		// and an empty desired lands on a non-empty system cgroup, which the v1
		// planner must reject. For a top-level reclaim root we therefore do NOT
		// auto-discover siblings at all; operators must declare any reclaim sibling
		// explicitly via BulkheadReclaimSiblingRelPaths. No host cgroup name is
		// hardcoded.
		if parentRel == "" {
			general.InfofV(4, "bulkhead: reclaim rel %q is at the cgroup root, skipping auto-discovered reclaim siblings", reclaimRel)
			continue
		}
		children, err := p.cgroup.ListChildren(ctx, parentRel)
		if err != nil {
			return nil, fmt.Errorf("list reclaim sibling parent %q: %w", parentRel, err)
		}
		for _, child := range children {
			rel := strings.Trim(path.Join(parentRel, child), "/")
			if rel == "" {
				continue
			}
			if _, skip := excluded[rel]; skip {
				continue
			}
			// The system_service plugin owns cfg.BulkheadSystemRelPath; do not
			// auto-discover it (or its subtree) as a reclaim sibling.
			if p.isSystemServiceOwnedRel(rel) {
				general.InfofV(4, "bulkhead: skipping system_service-owned rel %q from auto-discovered reclaim siblings", rel)
				continue
			}
			if p.isConfiguredReclaimNUMARel(rel) {
				continue
			}
			if _, ok := seen[rel]; ok {
				continue
			}
			seen[rel] = struct{}{}
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (p *CPUSetTopologyPlugin) configuredBulkheadReclaimSiblings() []string {
	out := make([]string, 0, len(p.cfg.BulkheadReclaimSiblingRelPaths))
	for _, rel := range p.cfg.BulkheadReclaimSiblingRelPaths {
		rel = strings.Trim(rel, "/")
		if rel == "" {
			continue
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}

func (p *CPUSetTopologyPlugin) mergeBulkheadReclaimSiblings(
	discovered []string,
	configured []string,
	view *model.CPUSetPartitionView,
) []string {
	var perNUMAReclaim map[int]machine.CPUSet
	if view != nil {
		perNUMAReclaim = view.ReclaimEffectivePerNUMA
	}
	excluded := p.reclaimSiblingExcludedSet(perNUMAReclaim)

	seen := map[string]struct{}{}
	out := make([]string, 0, len(discovered)+len(configured))
	add := func(rel string) bool {
		rel = strings.Trim(rel, "/")
		if rel == "" {
			return false
		}
		if _, skip := excluded[rel]; skip {
			return false
		}
		if p.isConfiguredReclaimNUMARel(rel) {
			return false
		}
		if _, ok := seen[rel]; ok {
			return false
		}
		seen[rel] = struct{}{}
		out = append(out, rel)
		return true
	}
	// Auto-discovered siblings must never include the system_service-owned
	// subtree (cpuset_topology must not be a second owner of the system domain).
	for _, rel := range discovered {
		if p.isSystemServiceOwnedRel(rel) {
			general.InfofV(4, "bulkhead: dropping auto-discovered system_service-owned rel %q from reclaim siblings", rel)
			continue
		}
		add(rel)
	}
	// Explicitly configured siblings are an operator contract and are honored
	// even when the rel coincides with the system rel path.
	for _, rel := range configured {
		add(rel)
	}
	sort.Strings(out)
	return out
}

func desiredCPUSetPartitionView(view *model.DesiredView) *model.CPUSetPartitionView {
	if view == nil {
		return nil
	}
	return &view.CPUSetPartitionView
}

func enableBulkheadCpusetTopology(in bulkheadapi.HandlerContext) bool {
	if in.State != nil && in.State.GetAllowSharedCoresOverlapReclaimedCores() {
		return false
	}
	return enableBulkheadCpusetTopologyByDynamicConf(in.DynamicConf)
}

func enableBulkheadCpusetTopologyByDynamicConf(conf *dynamicconfig.Configuration) bool {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.EnableBulkheadCpusetTopology
}

func (p *CPUSetTopologyPlugin) isConfiguredReclaimNUMARel(rel string) bool {
	rel = strings.Trim(rel, "/")
	for _, prefix := range p.cfg.BulkheadReclaimNumaPrefixes {
		prefix = strings.Trim(prefix, "/")
		if prefix == "" || !strings.HasPrefix(rel, prefix) {
			continue
		}
		suffix := strings.TrimPrefix(rel, prefix)
		if suffix == "" {
			continue
		}
		if _, err := strconv.Atoi(suffix); err == nil {
			return true
		}
	}
	return false
}

// isSystemServiceOwnedRel reports whether rel belongs to the cgroup subtree
// owned by the independent system_service plugin (cfg.BulkheadSystemRelPath).
// cpuset_topology must not become a second owner of that subtree: doing so
// projects ReclaimEffective onto the system domain and, during shared ramp-up
// when ReclaimEffective shrinks, emits an empty desired onto a non-empty system
// cgroup that the v1 planner rejects. The exclusion applies to auto-discovered
// siblings only; an operator who lists the rel explicitly in
// BulkheadReclaimSiblingRelPaths has opted in and is honored by merge.
func (p *CPUSetTopologyPlugin) isSystemServiceOwnedRel(rel string) bool {
	systemRel := strings.Trim(p.cfg.BulkheadSystemRelPath, "/")
	if systemRel == "" {
		return false
	}
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return false
	}
	return rel == systemRel || strings.HasPrefix(rel, systemRel+"/")
}

func topologyBudgetFromConfig(in bulkheadconfig.ConvergenceBudget) topology.ConvergenceBudget {
	return topology.ConvergenceBudget{
		MaxRounds:                  in.MaxRounds,
		MaxHierarchyIOOperations:   in.MaxHierarchyIOOperations,
		MaxSnapshotNodes:           in.MaxSnapshotNodes,
		MaxSnapshotDepth:           in.MaxSnapshotDepth,
		MaxDomains:                 in.MaxDomains,
		MaxTransferEdges:           in.MaxTransferEdges,
		MaxPlanOperations:          in.MaxPlanOperations,
		MaxDeadlockProbeOperations: in.MaxDeadlockProbeOperations,
		DeadlineDuration:           in.DeadlineDuration,
	}
}

func topologyDrainSelectionFromConfig(in bulkheadconfig.DrainSelectionPolicy) topology.DrainSelectionPolicy {
	return topology.DrainSelectionPolicy{
		MaxCPUsDrainRatio:         in.MaxCPUsDrainRatio,
		GroupByNUMA:               in.GroupByNUMA,
		RequirePairedSwapProgress: in.RequirePairedSwapProgress,
	}
}

const (
	metricBulkheadPruneResult                  = "bulkhead_prune_result"
	metricBulkheadPruneActiveRels              = "bulkhead_prune_active_rels"
	metricBulkheadTopologyRoundTotal           = "bulkhead_topology_round_total"
	metricBulkheadTopologyRoundsPerApply       = "bulkhead_topology_rounds_per_apply"
	metricBulkheadTopologyBudgetExhaustedTotal = "bulkhead_topology_budget_exhausted_total"
	metricBulkheadTopologyScanNodes            = "bulkhead_topology_scan_nodes"
	metricBulkheadTopologyScanDepth            = "bulkhead_topology_scan_depth"
	metricBulkheadTopologyDrainBatch           = "bulkhead_topology_drain_batch"
	metricBulkheadTopologyIdentityChangedTotal = "bulkhead_topology_identity_changed_total"
)

var (
	allowedTopologyMetricPhases      = map[string]struct{}{"normal": {}, "reclaim_only": {}, "reset": {}}
	allowedTopologyMetricStatuses    = map[string]struct{}{"progress": {}, "stale": {}, "blocked": {}, "converged": {}, "error": {}}
	allowedTopologyMetricReasons     = map[string]struct{}{"none": {}, "stale": {}, "blocked": {}, "budget": {}, "identity_changed": {}, "external_write": {}, "invalid": {}}
	allowedTopologyMetricDomainRoles = map[string]struct{}{"primary": {}, "reclaim": {}, "reclaim_numa": {}, "dynamic": {}, "unknown": {}}
	allowedPruneMetricStatuses       = map[string]struct{}{"success": {}, "skipped": {}}
	allowedPruneMetricReasons        = map[string]struct{}{
		"none": {}, "container_error": {}, "view_error": {}, "discover_error": {},
		"dag_error": {}, "not_converged": {}, "deferred_convergence": {},
		"reset_target_error": {}, "reset_not_converged": {}, "invalid": {},
	}
)

func emitBulkheadTopologySummary(emitter metrics.MetricEmitter, phase string, res topology.ConvergenceResult, err error) {
	if emitter == nil {
		return
	}
	phase = boundedTopologyLabel(phase, allowedTopologyMetricPhases, "normal")
	status := "converged"
	reason := "none"
	if err != nil {
		status = "error"
		reason = topologyErrorReason(err)
	} else if !res.Converged {
		status = string(res.State)
		reason = topologyStateReason(res.State)
	}
	status = boundedTopologyLabel(status, allowedTopologyMetricStatuses, "error")
	reason = boundedTopologyLabel(reason, allowedTopologyMetricReasons, "invalid")
	general.Infof("cpuset_topology: apply summary phase=%s status=%s reason=%s rounds=%d applied=%d skipped=%d failed=%d deferred=%d first_blocker=%q",
		phase, status, reason, len(res.Rounds), res.Applied, res.Skipped, res.Failed, res.Deferred, res.FirstBlocker())

	_ = emitter.StoreInt64(metricBulkheadTopologyRoundsPerApply, int64(len(res.Rounds)), metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "phase", Val: phase},
		metrics.MetricTag{Key: "status", Val: status},
		metrics.MetricTag{Key: "reason", Val: reason},
	)
	for _, round := range res.Rounds {
		roundStatus := boundedTopologyLabel(string(round.Status), allowedTopologyMetricStatuses, status)
		roundReason := topologyRoundReason(round.Status)
		_ = emitter.StoreInt64(metricBulkheadTopologyRoundTotal, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "phase", Val: phase},
			metrics.MetricTag{Key: "status", Val: roundStatus},
			metrics.MetricTag{Key: "reason", Val: roundReason},
		)
		_ = emitter.StoreInt64(metricBulkheadTopologyScanNodes, int64(round.Cost.Nodes), metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "phase", Val: phase},
			metrics.MetricTag{Key: "status", Val: roundStatus},
			metrics.MetricTag{Key: "reason", Val: roundReason},
		)
		_ = emitter.StoreInt64(metricBulkheadTopologyScanDepth, int64(round.Cost.MaxDepth), metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "phase", Val: phase},
			metrics.MetricTag{Key: "status", Val: roundStatus},
			metrics.MetricTag{Key: "reason", Val: roundReason},
		)
		drained := 0
		for _, witness := range round.Witnesses {
			drained += witness.CPUs.Size()
		}
		_ = emitter.StoreInt64(metricBulkheadTopologyDrainBatch, int64(drained), metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "phase", Val: phase},
			metrics.MetricTag{Key: "domain_role", Val: "unknown"},
		)
	}
	if reason == "budget" {
		_ = emitter.StoreInt64(metricBulkheadTopologyBudgetExhaustedTotal, 1, metrics.MetricTypeNameCount,
			metrics.MetricTag{Key: "kind", Val: topologyBudgetKind(err)},
		)
	}
	if reason == "identity_changed" {
		_ = emitter.StoreInt64(metricBulkheadTopologyIdentityChangedTotal, 1, metrics.MetricTypeNameCount)
	}
}

func topologyStateReason(state topology.ConvergenceState) string {
	switch state {
	case topology.ConvergenceStateBlocked:
		return "blocked"
	case topology.ConvergenceStateNonConverged:
		return "stale"
	default:
		return "none"
	}
}

func topologyRoundReason(status topology.RoundStatus) string {
	switch status {
	case topology.RoundStatusStale:
		return "stale"
	case topology.RoundStatusBlocked:
		return "blocked"
	default:
		return "none"
	}
}

func topologyErrorReason(err error) string {
	switch {
	case errors.Is(err, topology.ErrRoundBudgetExceeded),
		errors.Is(err, topology.ErrHierarchyIOOperationBudgetExceeded),
		errors.Is(err, topology.ErrNodeBudgetExceeded),
		errors.Is(err, topology.ErrHierarchyDepthBudget),
		errors.Is(err, topology.ErrDomainBudgetExceeded),
		errors.Is(err, topology.ErrTransferEdgeBudgetExceeded),
		errors.Is(err, topology.ErrPlanOperationBudgetExceeded),
		errors.Is(err, topology.ErrDeadlockProbeBudgetExceeded),
		errors.Is(err, topology.ErrConvergenceDeadlineExceeded),
		errors.Is(err, topology.ErrAdjustmentReplanBudgetExceeded),
		errors.Is(err, topology.ErrAdjustmentWriteBudgetExceeded),
		errors.Is(err, topology.ErrAdjustmentDeadlineExceeded),
		errors.Is(err, context.Canceled),
		errors.Is(err, context.DeadlineExceeded):
		return "budget"
	case errors.Is(err, topology.ErrCgroupIdentityChanged):
		return "identity_changed"
	default:
		return "invalid"
	}
}

func topologyBudgetKind(err error) string {
	switch {
	case errors.Is(err, topology.ErrRoundBudgetExceeded):
		return "round"
	case errors.Is(err, topology.ErrHierarchyIOOperationBudgetExceeded):
		return "hierarchy_io"
	case errors.Is(err, topology.ErrNodeBudgetExceeded):
		return "node"
	case errors.Is(err, topology.ErrHierarchyDepthBudget):
		return "depth"
	case errors.Is(err, topology.ErrDomainBudgetExceeded):
		return "domain"
	case errors.Is(err, topology.ErrTransferEdgeBudgetExceeded):
		return "edge"
	case errors.Is(err, topology.ErrPlanOperationBudgetExceeded):
		return "operation"
	case errors.Is(err, topology.ErrDeadlockProbeBudgetExceeded):
		return "deadlock_probe"
	case errors.Is(err, topology.ErrAdjustmentReplanBudgetExceeded):
		return "adjustment_replan"
	case errors.Is(err, topology.ErrAdjustmentWriteBudgetExceeded):
		return "adjustment_write"
	case errors.Is(err, topology.ErrAdjustmentDeadlineExceeded):
		return "adjustment_deadline"
	case errors.Is(err, topology.ErrConvergenceDeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	default:
		return "context"
	}
}

func boundedTopologyLabel(value string, allowed map[string]struct{}, fallback string) string {
	if _, ok := allowed[value]; ok {
		return value
	}
	return fallback
}

func normalizePruneMetricLabels(status, reason string) (string, string) {
	status = boundedTopologyLabel(status, allowedPruneMetricStatuses, "skipped")
	if reason == "" {
		reason = "none"
	}
	return status, boundedTopologyLabel(reason, allowedPruneMetricReasons, "invalid")
}

func emitBulkheadPruneResult(emitter metrics.MetricEmitter, status, reason string) {
	if emitter == nil {
		return
	}
	status, reason = normalizePruneMetricLabels(status, reason)
	_ = emitter.StoreInt64(metricBulkheadPruneResult, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "status", Val: status},
		metrics.MetricTag{Key: "reason", Val: reason},
	)
}

func emitBulkheadPruneActiveRels(
	emitter metrics.MetricEmitter,
	activeRelsCount int,
	status, reason string,
) {
	if emitter == nil {
		return
	}
	status, reason = normalizePruneMetricLabels(status, reason)
	_ = emitter.StoreInt64(metricBulkheadPruneActiveRels, int64(activeRelsCount), metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "status", Val: status},
		metrics.MetricTag{Key: "reason", Val: reason},
	)
}
