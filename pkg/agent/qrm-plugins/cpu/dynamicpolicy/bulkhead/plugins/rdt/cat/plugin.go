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

package cat

import (
	"context"
	"errors"
	"fmt"
	"math/bits"
	"sort"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/util/errors"

	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	qrmresctrlmanager "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
	qrmresctrl "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/resctrl"
	"github.com/kubewharf/katalyst-core/pkg/util/external/rdt"
	resctrlutil "github.com/kubewharf/katalyst-core/pkg/util/resctrl"
)

const CATPluginName = "rdt_cat"

var (
	_ bulkheadapi.Plugin            = (*CATPlugin)(nil)
	_ bulkheadapi.PeriodicalCapable = (*CATPlugin)(nil)
)

type closLister interface {
	ListManagedClos(context.Context) ([]qrmresctrlmanager.CPUListClos, error)
}

type CATPlugin struct {
	config             *qrmresctrl.ResctrlConfig
	closLister         closLister
	rdtManager         rdt.RDTManager
	capabilityProvider rdt.CATCapabilityProvider
}

func NewCATPlugin(conf *config.Configuration) bulkheadapi.Plugin {
	var resctrlConfig *qrmresctrl.ResctrlConfig
	if conf != nil && conf.QRMPluginsConfiguration != nil {
		resctrlConfig = conf.QRMPluginsConfiguration.ResctrlConfig
	}
	if resctrlConfig == nil {
		resctrlConfig = qrmresctrl.NewResctrlConfig()
	} else {
		configCopy := *resctrlConfig
		resctrlConfig = &configCopy
	}
	return NewCATPluginWithManager(
		resctrlConfig,
		qrmresctrlmanager.NewCPUListManager(),
		rdt.NewDefaultManager(),
		rdt.NewCATCapabilityProvider(),
	)
}

func NewCATPluginWithManager(
	config *qrmresctrl.ResctrlConfig,
	closLister closLister,
	rdtManager rdt.RDTManager,
	capabilityProvider rdt.CATCapabilityProvider,
) *CATPlugin {
	if config == nil {
		config = qrmresctrl.NewResctrlConfig()
	}
	return &CATPlugin{
		config:             config,
		closLister:         closLister,
		rdtManager:         rdtManager,
		capabilityProvider: capabilityProvider,
	}
}

func (p *CATPlugin) Name() string { return CATPluginName }

func (p *CATPlugin) Enable(in bulkheadapi.HandlerContext) bool {
	return enableCAT(in.DynamicConf)
}

func (p *CATPlugin) PeriodicalHandler(ctx context.Context, in bulkheadapi.PeriodicalHandlerContext) error {
	if p.closLister == nil || p.rdtManager == nil || p.capabilityProvider == nil {
		return nil
	}
	if disableRDT(in.DynamicConf) {
		return nil
	}
	cat := catConfig(in.DynamicConf)
	if enableCAT(in.DynamicConf) {
		if cat.CATConfigError != "" {
			return fmt.Errorf("invalid cat configuration: %s", cat.CATConfigError)
		}
		_, err := p.reconcileConfig(ctx, cat)
		if err != nil {
			return err
		}
		return nil
	}
	if err := p.rollback(ctx, cat.DefaultCATWays); err != nil {
		return err
	}
	return nil
}

type catTarget struct {
	clos string
	mask map[int]uint64
}

func (p *CATPlugin) reconcileConfig(ctx context.Context, conf qrmconfig.DynamicBulkheadRDTConfiguration) (bool, error) {
	capabilities, err := p.capabilityProvider.GetCATCapabilities()
	if err != nil {
		if errors.Is(err, rdt.ErrCATUnsupported) {
			return false, nil
		}
		return false, fmt.Errorf("get CAT capabilities: %w", err)
	}
	clos, err := p.closLister.ListManagedClos(ctx)
	if err != nil {
		return false, fmt.Errorf("list CLOS: %w", err)
	}
	resolved, err := p.resolveExpressionOverrides(conf.ClosCATWays)
	if err != nil {
		return false, err
	}
	policy, err := p.resolveCATPolicy(conf.CATPolicy)
	if err != nil {
		return false, err
	}
	targets, err := buildTargets(capabilities, clos, conf.DefaultCATWays, resolved, policy)
	if err != nil {
		return false, err
	}
	for _, target := range targets {
		if err := p.rdtManager.ApplyCAT(target.clos, target.mask); err != nil {
			applyErr := fmt.Errorf("apply CAT for CLOS %q: %w", target.clos, err)
			rollbackTarget, targetErr := catRollbackTarget(capabilities, conf.DefaultCATWays)
			if targetErr != nil {
				return false, apierrors.NewAggregate([]error{applyErr, fmt.Errorf("build CAT rollback target: %w", targetErr)})
			}
			if rollbackErr := p.applyTargetToClos(clos, rollbackTarget); rollbackErr != nil {
				return false, apierrors.NewAggregate([]error{applyErr, rollbackErr})
			}
			return false, applyErr
		}
	}
	return len(targets) > 0, nil
}

func (p *CATPlugin) rollback(ctx context.Context, defaultWays qrmconfig.CATWaysExpression) error {
	capabilities, err := p.capabilityProvider.GetCATCapabilities()
	if err != nil {
		if errors.Is(err, rdt.ErrCATUnsupported) {
			return nil
		}
		return fmt.Errorf("get CAT capabilities: %w", err)
	}
	clos, err := p.closLister.ListManagedClos(ctx)
	if err != nil {
		return fmt.Errorf("list CLOS for rollback: %w", err)
	}
	target, err := catRollbackTarget(capabilities, defaultWays)
	if err != nil {
		return fmt.Errorf("build CAT rollback target: %w", err)
	}
	return p.applyTargetToClos(clos, target)
}

func catRollbackTarget(capabilities map[int]rdt.CATCapability, defaultWays qrmconfig.CATWaysExpression) (map[int]uint64, error) {
	if defaultWays.Configured() {
		return expressionTarget(capabilities, defaultWays, qrmconfig.CATPlacementPolicy{})
	}
	if len(capabilities) == 0 {
		return nil, fmt.Errorf("no L3 CAT domains")
	}
	target := make(map[int]uint64, len(capabilities))
	for domain, capability := range capabilities {
		if !isContiguousMask(capability.CBMMask) {
			return nil, fmt.Errorf("domain %d has non-contiguous CAT capability mask %x", domain, capability.CBMMask)
		}
		if bits.OnesCount64(capability.CBMMask) < capability.MinCBMBits {
			return nil, fmt.Errorf("domain %d CAT capability mask %x is smaller than minimum %d",
				domain, capability.CBMMask, capability.MinCBMBits)
		}
		target[domain] = capability.CBMMask
	}
	return target, nil
}

func (p *CATPlugin) applyTargetToClos(clos []qrmresctrlmanager.CPUListClos, target map[int]uint64) error {
	var errs []error
	for _, current := range clos {
		if err := p.rdtManager.ApplyCAT(current.ID, target); err != nil {
			errs = append(errs, fmt.Errorf("restore CAT for CLOS %q: %w", current.ID, err))
		}
	}
	return apierrors.NewAggregate(errs)
}

func (p *CATPlugin) resolveExpressionOverrides(overrides map[string]qrmconfig.CATWaysExpression) (map[string]qrmconfig.CATWaysExpression, error) {
	resolved := make(map[string]qrmconfig.CATWaysExpression, len(overrides))
	directClosIDs := make(map[string]struct{}, len(overrides))
	for key := range overrides {
		if resctrlutil.IsExplicitSharedPoolMapping(key, p.config) {
			continue
		}
		directClosIDs[resctrlutil.ResolveCATWayKey(key, p.config)] = struct{}{}
	}
	for key, expr := range overrides {
		closID := resctrlutil.ResolveCATWayKey(key, p.config)
		if resctrlutil.IsExplicitSharedPoolMapping(key, p.config) {
			if _, direct := directClosIDs[closID]; direct {
				continue
			}
		}
		if existing, ok := resolved[closID]; ok && existing.String() != expr.String() {
			return nil, fmt.Errorf("conflicting CAT way configuration for CLOS %q", closID)
		}
		resolved[closID] = expr
	}
	return resolved, nil
}

func (p *CATPlugin) resolveCATPolicy(policy qrmconfig.CATPolicy) (qrmconfig.CATPolicy, error) {
	resolved := qrmconfig.CATPolicy{
		DefaultPlacement: policy.DefaultPlacement,
	}
	if policy.ClosPlacements != nil {
		resolved.ClosPlacements = make(map[string]qrmconfig.CATPlacementPolicy, len(policy.ClosPlacements))
		for key, placement := range policy.ClosPlacements {
			closID := resctrlutil.ResolveCATWayKey(key, p.config)
			if _, ok := resolved.ClosPlacements[closID]; ok {
				return qrmconfig.CATPolicy{}, fmt.Errorf("duplicate CAT placement for CLOS %q", closID)
			}
			resolved.ClosPlacements[closID] = placement
		}
	}
	if policy.ExclusiveClosIDs != nil {
		seen := map[string]struct{}{}
		resolved.ExclusiveClosIDs = make([]string, 0, len(policy.ExclusiveClosIDs))
		for _, closID := range policy.ExclusiveClosIDs {
			if strings.TrimSpace(closID) == "" || strings.ContainsAny(closID, " \t\n\r*") {
				return qrmconfig.CATPolicy{}, fmt.Errorf("exclusive CLOS ID %q must be exact", closID)
			}
			if _, ok := seen[closID]; ok {
				return qrmconfig.CATPolicy{}, fmt.Errorf("duplicate exclusive CLOS ID %q", closID)
			}
			seen[closID] = struct{}{}
			resolved.ExclusiveClosIDs = append(resolved.ExclusiveClosIDs, closID)
		}
	}
	return resolved, nil
}

func expressionTarget(capabilities map[int]rdt.CATCapability, expr qrmconfig.CATWaysExpression, placement qrmconfig.CATPlacementPolicy) (map[int]uint64, error) {
	if !expr.Configured() {
		return nil, fmt.Errorf("CAT ways expression is not configured")
	}
	if len(capabilities) == 0 {
		return nil, fmt.Errorf("no L3 CAT domains")
	}
	target := make(map[int]uint64, len(capabilities))
	for _, domain := range sortedDomains(capabilities) {
		capability := capabilities[domain]
		mask, err := targetForDomain(domain, capability, expr, placement)
		if err != nil {
			return nil, err
		}
		target[domain] = mask
	}
	return target, nil
}

func targetForDomain(domain int, capability rdt.CATCapability, expr qrmconfig.CATWaysExpression, placement qrmconfig.CATPlacementPolicy) (uint64, error) {
	available, err := allowedMask(capability, placement.AllowedBitUsages)
	if err != nil {
		return 0, fmt.Errorf("domain %d: %w", domain, err)
	}
	return targetForAvailable(domain, capability, expr, placement.Direction, available)
}

func targetForAvailable(domain int, capability rdt.CATCapability, expr qrmconfig.CATWaysExpression, direction qrmconfig.CATAllocationDirection, available uint64) (uint64, error) {
	if !isContiguousMask(capability.CBMMask) {
		return 0, fmt.Errorf("domain %d has non-contiguous CAT capability mask %x", domain, capability.CBMMask)
	}
	maxWays := int64(bits.OnesCount64(capability.CBMMask))
	ways, err := expr.Evaluate(maxWays, int64(capability.MinCBMBits))
	if err != nil {
		return 0, fmt.Errorf("domain %d cannot evaluate CAT ways expression %q: %w", domain, expr.String(), err)
	}
	if ways < 0 {
		return 0, fmt.Errorf("CAT ways must be non-negative, got %d", ways)
	}
	if ways == 0 {
		if capability.MinCBMBits != 0 {
			return 0, fmt.Errorf("domain %d does not support zero CAT ways", domain)
		}
		return 0, nil
	}
	if ways < int64(capability.MinCBMBits) || ways > int64(bits.OnesCount64(available)) {
		return 0, fmt.Errorf("domain %d cannot satisfy %d CAT ways", domain, ways)
	}
	mask, ok := contiguousMaskWithDirection(available, int(ways), direction)
	if !ok {
		return 0, fmt.Errorf("domain %d has no contiguous mask with %d CAT ways", domain, ways)
	}
	return mask, nil
}

func allowedMask(capability rdt.CATCapability, usages []qrmconfig.CATBitUsage) (uint64, error) {
	if len(usages) == 0 {
		return capability.CBMMask, nil
	}
	var mask uint64
	for _, usage := range usages {
		if usage == qrmconfig.CATBitUsageAll {
			return capability.CBMMask, nil
		}
		usageMask, ok := capability.BitUsageByType[string(usage)]
		if !ok {
			return 0, fmt.Errorf("cat bit usage %q is unavailable", usage)
		}
		mask |= usageMask
	}
	return capability.CBMMask & mask, nil
}

func contiguousMaskWithDirection(available uint64, ways int, direction qrmconfig.CATAllocationDirection) (uint64, bool) {
	if ways <= 0 || ways > 64 {
		return 0, false
	}
	if direction == qrmconfig.CATAllocationDirectionHigh {
		for start := 64 - ways; start >= 0; start-- {
			mask := ^uint64(0)
			if ways < 64 {
				mask = (uint64(1)<<ways - 1) << start
			}
			if mask&^available == 0 {
				return mask, true
			}
		}
		return 0, false
	}
	for start := 0; start+ways <= 64; start++ {
		mask := ^uint64(0)
		if ways < 64 {
			mask = (uint64(1)<<ways - 1) << start
		}
		if mask&^available == 0 {
			return mask, true
		}
	}
	return 0, false
}

func isContiguousMask(mask uint64) bool {
	if mask == 0 {
		return false
	}
	lowest := mask & -mask
	return (mask+lowest)&mask == 0
}

func buildTargets(
	capabilities map[int]rdt.CATCapability,
	clos []qrmresctrlmanager.CPUListClos,
	defaultExpr qrmconfig.CATWaysExpression,
	overrides map[string]qrmconfig.CATWaysExpression,
	policy qrmconfig.CATPolicy,
) ([]catTarget, error) {
	managed := make(map[string]struct{}, len(clos))
	expressions := make(map[string]qrmconfig.CATWaysExpression, len(clos))
	for _, current := range clos {
		managed[current.ID] = struct{}{}
		expr := defaultExpr
		if override, ok := overrides[current.ID]; ok {
			expr = override
		}
		expressions[current.ID] = expr
	}

	domains := sortedDomains(capabilities)
	targets := make(map[string]map[int]uint64, len(clos))
	remaining := make(map[int]uint64, len(domains))
	for _, domain := range domains {
		remaining[domain] = capabilities[domain].CBMMask
	}
	exclusive := map[string]struct{}{}
	for _, closID := range policy.ExclusiveClosIDs {
		if _, ok := managed[closID]; !ok {
			return nil, fmt.Errorf("exclusive CLOS %q is not configured", closID)
		}
		if _, ok := exclusive[closID]; ok {
			return nil, fmt.Errorf("duplicate exclusive CLOS ID %q", closID)
		}
		exclusive[closID] = struct{}{}
		target, err := expressionTargetWithAvailable(capabilities, domains, expressions[closID], placementForCLOS(policy, closID), remaining)
		if err != nil {
			return nil, fmt.Errorf("build exclusive CAT target for CLOS %q: %w", closID, err)
		}
		targets[closID] = target
		for domain, mask := range target {
			remaining[domain] &^= mask
		}
	}

	for _, current := range clos {
		if _, ok := exclusive[current.ID]; ok {
			continue
		}
		target, err := expressionTargetWithAvailable(capabilities, domains, expressions[current.ID], placementForCLOS(policy, current.ID), remaining)
		if err != nil {
			return nil, fmt.Errorf("build CAT target for CLOS %q: %w", current.ID, err)
		}
		targets[current.ID] = target
		// Reserve this CLOS's allocation from the shared remaining pool so
		// multiple non-exclusive CLOSes pack into disjoint cache ways instead
		// of every one independently claiming the same free slot.
		for domain, mask := range target {
			remaining[domain] &^= mask
		}
	}

	ids := make([]string, 0, len(targets))
	for id := range targets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := make([]catTarget, 0, len(ids))
	for _, id := range ids {
		result = append(result, catTarget{clos: id, mask: targets[id]})
	}
	return result, nil
}

func expressionTargetWithAvailable(
	capabilities map[int]rdt.CATCapability,
	domains []int,
	expr qrmconfig.CATWaysExpression,
	placement qrmconfig.CATPlacementPolicy,
	available map[int]uint64,
) (map[int]uint64, error) {
	if !expr.Configured() {
		return nil, fmt.Errorf("CAT ways expression is not configured")
	}
	target := make(map[int]uint64, len(domains))
	for _, domain := range domains {
		domainAvailable := available[domain]
		if len(placement.AllowedBitUsages) > 0 {
			usageMask, err := allowedMask(capabilities[domain], placement.AllowedBitUsages)
			if err != nil {
				return nil, fmt.Errorf("domain %d: %w", domain, err)
			}
			domainAvailable &= usageMask
		}
		mask, err := targetForAvailable(domain, capabilities[domain], expr, placement.Direction, domainAvailable)
		if err != nil {
			return nil, err
		}
		target[domain] = mask
	}
	return target, nil
}

func placementForCLOS(policy qrmconfig.CATPolicy, closID string) qrmconfig.CATPlacementPolicy {
	placement := qrmconfig.CATPlacementPolicy{}
	if policy.DefaultPlacement != nil {
		placement = *policy.DefaultPlacement
	}
	if override, ok := policy.ClosPlacements[closID]; ok {
		placement = override
	}
	return placement
}

func sortedDomains(capabilities map[int]rdt.CATCapability) []int {
	domains := make([]int, 0, len(capabilities))
	for domain := range capabilities {
		domains = append(domains, domain)
	}
	sort.Ints(domains)
	return domains
}

func enableCAT(conf *dynamicconfig.Configuration) bool {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	if disableRDT(conf) {
		return false
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig.EnableCAT
}

func disableRDT(conf *dynamicconfig.Configuration) bool {
	return conf != nil &&
		conf.AdminQoSConfiguration != nil &&
		conf.AdminQoSConfiguration.QRMPluginConfiguration != nil &&
		conf.AdminQoSConfiguration.QRMPluginConfiguration.RDTConfig.DisableRDT
}

func catConfig(conf *dynamicconfig.Configuration) qrmconfig.DynamicBulkheadRDTConfiguration {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return qrmconfig.DynamicBulkheadRDTConfiguration{}
	}
	cat := conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.BulkheadRDTConfig
	return cat
}
