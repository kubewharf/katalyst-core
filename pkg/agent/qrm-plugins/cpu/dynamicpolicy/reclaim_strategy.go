/*
Copyright 2026 The Katalyst Authors.

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

package dynamicpolicy

import (
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
)

// ReclaimStrategyName identifies a reclaim strategy family. Adding a new reclaim
// strategy should be a single self-registering entry here, instead of fanning out
// across the advisor/allocation/hard-reclaim handlers.
type ReclaimStrategyName string

const (
	// ReclaimStrategyHardPartitionRampUp is the disjoint, immutable per-NUMA
	// hard-partition reclaim imposed while ramp-up domains are active. It maps
	// onto the historical `hardActive` decision and its gate is
	// isRampUpReclaimHardPartitionEnabledWithConfig.
	ReclaimStrategyHardPartitionRampUp ReclaimStrategyName = "hard_partition_ramp_up"

	// ReclaimStrategySteadyFakeNUMAReclaim is the steady (non-hard-active) path
	// that materializes a fake-NUMA mandatory reclaim block. At config level it
	// is permitted whenever node reclaim is on and hard partition is not; the
	// per-attempt activation additionally requires a fake-NUMA mandatory reclaim
	// block in the advisor response (advisorResponseHasFakeNUMAMandatoryReclaim),
	// which remains a runtime gate in the advisor handler.
	ReclaimStrategySteadyFakeNUMAReclaim ReclaimStrategyName = "steady_fake_numa_reclaim"

	// ReclaimStrategyLegacyOverlapReclaim is the historical overlap reclaim
	// fallback used when neither the hard-partition nor the steady fake-NUMA
	// path applies.
	ReclaimStrategyLegacyOverlapReclaim ReclaimStrategyName = "legacy_overlap_reclaim"
)

// reclaimStrategyPrecedence orders strategies from most specific / highest
// priority to the fallback. It is the single source of truth for which strategy
// "wins" when more than one is config-gated.
var reclaimStrategyPrecedence = []ReclaimStrategyName{
	ReclaimStrategyHardPartitionRampUp,
	ReclaimStrategySteadyFakeNUMAReclaim,
	ReclaimStrategyLegacyOverlapReclaim,
}

// ReclaimStrategy describes one reclaim strategy family. The interface is
// deliberately narrow: it only captures the *config-level gate*, because the
// execution paths for the existing strategies still share the DynamicPolicy
// state machine (advisor block planner, apply, checkpoint transition). Moving
// execution behind this interface is a future migration; see the package-level
// docs below.
type ReclaimStrategy interface {
	// Name returns the stable strategy identifier.
	Name() ReclaimStrategyName
	// Description returns a short human-readable note for logs / observability.
	Description() string
	// ConfigGated reports whether the frozen dynamic configuration permits this
	// strategy family. It must be pure: no state reads, no errors, identical to
	// the predicate the existing handler already evaluated inline.
	ConfigGated(dyn *dynamicconfig.Configuration) bool
}

// reclaimStrategies is the registry. Strategies self-register in init() so that
// adding a new reclaim strategy is a single file + one registerReclaimStrategy
// call, rather than edits across the handler dispatch sites.
var reclaimStrategies = map[ReclaimStrategyName]ReclaimStrategy{}

// registerReclaimStrategy adds a strategy to the registry. It is called from
// each strategy's init() and panics on duplicate names so registration bugs
// surface at startup, not at runtime.
func registerReclaimStrategy(s ReclaimStrategy) {
	if s == nil || s.Name() == "" {
		panic("registerReclaimStrategy: strategy must have a non-empty name")
	}
	if _, exists := reclaimStrategies[s.Name()]; exists {
		panic("registerReclaimStrategy: duplicate strategy registered: " + string(s.Name()))
	}
	reclaimStrategies[s.Name()] = s
}

// lookupReclaimStrategy returns the registered strategy by name, if any.
func lookupReclaimStrategy(name ReclaimStrategyName) (ReclaimStrategy, bool) {
	s, ok := reclaimStrategies[name]
	return s, ok
}

// hardPartitionReclaimConfigGated consults the registered hard-partition
// strategy for its config gate. If the strategy were somehow unregistered it
// falls back to the legacy predicate, so the lookup can never silently flip the
// gate. This is the single live control-flow use of the registry; the other
// strategies keep their inline runtime gates.
func hardPartitionReclaimConfigGated(dyn *dynamicconfig.Configuration) bool {
	if s, ok := lookupReclaimStrategy(ReclaimStrategyHardPartitionRampUp); ok {
		return s.ConfigGated(dyn)
	}
	return isRampUpReclaimHardPartitionEnabledWithConfig(dyn)
}

// RegisteredReclaimStrategies returns the registered strategies in precedence
// order, so callers and tests see a stable, deterministic inventory.
func RegisteredReclaimStrategies() []ReclaimStrategy {
	out := make([]ReclaimStrategy, 0, len(reclaimStrategies))
	for _, name := range reclaimStrategyPrecedence {
		if s, ok := reclaimStrategies[name]; ok {
			out = append(out, s)
		}
	}
	return out
}

// activeReclaimStrategyNames returns the strategy families permitted by the
// frozen dynamic config, in precedence order. This is an observability /
// dispatch-inventory helper: the existing control flow still evaluates the same
// predicates inline; this function makes the strategy set inspectable and gives
// new strategies a registration point.
func activeReclaimStrategyNames(dyn *dynamicconfig.Configuration) []ReclaimStrategyName {
	out := make([]ReclaimStrategyName, 0, len(reclaimStrategyPrecedence))
	for _, s := range RegisteredReclaimStrategies() {
		if s.ConfigGated(dyn) {
			out = append(out, s.Name())
		}
	}
	return out
}

// --- built-in strategies -----------------------------------------------------
//
// The three built-in strategies wrap the predicates the handlers already used
// inline. The hard-partition gate is additionally wired into the advisor
// handler's `hardPartitionEnabled` derivation, so the registry is live rather
// than dead inventory. The steady / legacy strategies keep their runtime gates
// in the handler; only their config-level family membership is registered.

type hardPartitionRampUpReclaimStrategy struct{}

func (s hardPartitionRampUpReclaimStrategy) Name() ReclaimStrategyName {
	return ReclaimStrategyHardPartitionRampUp
}

func (s hardPartitionRampUpReclaimStrategy) Description() string {
	return "disjoint immutable per-NUMA hard-partition reclaim while ramp-up domains are active"
}

func (s hardPartitionRampUpReclaimStrategy) ConfigGated(dyn *dynamicconfig.Configuration) bool {
	return isRampUpReclaimHardPartitionEnabledWithConfig(dyn)
}

type steadyFakeNUMAReclaimStrategy struct{}

func (s steadyFakeNUMAReclaimStrategy) Name() ReclaimStrategyName {
	return ReclaimStrategySteadyFakeNUMAReclaim
}

func (s steadyFakeNUMAReclaimStrategy) Description() string {
	return "steady non-hard-active fake-NUMA mandatory reclaim materialization"
}

func (s steadyFakeNUMAReclaimStrategy) ConfigGated(dyn *dynamicconfig.Configuration) bool {
	// Permitted whenever node reclaim is on and the hard-partition disjoint mode
	// is not. The actual per-attempt activation still requires the advisor
	// response to carry a fake-NUMA mandatory reclaim block.
	return dyn != nil && dyn.EnableReclaim &&
		!isRampUpReclaimHardPartitionEnabledWithConfig(dyn)
}

type legacyOverlapReclaimStrategy struct{}

func (s legacyOverlapReclaimStrategy) Name() ReclaimStrategyName {
	return ReclaimStrategyLegacyOverlapReclaim
}

func (s legacyOverlapReclaimStrategy) Description() string {
	return "historical overlap reclaim fallback"
}

func (s legacyOverlapReclaimStrategy) ConfigGated(dyn *dynamicconfig.Configuration) bool {
	return dyn != nil && dyn.EnableReclaim &&
		!isRampUpReclaimHardPartitionEnabledWithConfig(dyn)
}

func init() {
	registerReclaimStrategy(hardPartitionRampUpReclaimStrategy{})
	registerReclaimStrategy(steadyFakeNUMAReclaimStrategy{})
	registerReclaimStrategy(legacyOverlapReclaimStrategy{})
}
