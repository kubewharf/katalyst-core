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

package resourcepackage

import (
	"fmt"
	"strings"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	OwnerPoolNameSeparator = "/"
)

// GetResourcePackageName retrieves the resource package name from pod annotations.
// It looks for the key "katalyst.kubewharf.io/resource_package" in the annotations map.
//
// Parameters:
//   - annotations: A map of pod annotations where to look for the resource package name.
//
// Returns:
//   - string: The resource package name if found, otherwise an empty string.
func GetResourcePackageName(annotations map[string]string) string {
	packageName, ok := annotations[consts.PodAnnotationResourcePackageKey]
	if !ok {
		return ""
	}

	return packageName
}

// WrapOwnerPoolName wraps the owner pool name with the package name.
// If the package name is empty, it returns the owner pool name as is.
// Otherwise, it prepends the package name to the owner pool name with a separator.
// Format: <pkgName>/<ownerPoolName>
func WrapOwnerPoolName(ownerPoolName, pkgName string) string {
	if pkgName == "" {
		return ownerPoolName
	}
	return pkgName + OwnerPoolNameSeparator + ownerPoolName
}

// UnwrapOwnerPoolName unwraps the owner pool name to get the original owner pool name and the package name.
// It splits the string by the last occurrence of the separator.
//
// Returns:
//   - string: The original owner pool name (suffix).
//   - string: The package name (prefix).
func UnwrapOwnerPoolName(ownerPoolName string) (string, string) {
	// Find the last index of the separator to split the name correctly.
	// This handles cases where the package name itself might contain the separator.
	// We assume the owner pool name does not contain the separator or we split by the last one.
	idx := strings.LastIndex(ownerPoolName, OwnerPoolNameSeparator)
	if idx == -1 {
		return ownerPoolName, ""
	}

	return ownerPoolName[idx+1:], ownerPoolName[:idx]
}

type suffixTranslatorWrapper struct {
	general.SuffixTranslator
}

// ResourcePackageSuffixTranslatorWrapper wraps a SuffixTranslator to handle resource package names in owner pool names.
// It ensures that the translation logic is applied to the base owner pool name, stripping the package suffix first.
func ResourcePackageSuffixTranslatorWrapper(translator general.SuffixTranslator) general.SuffixTranslator {
	return &suffixTranslatorWrapper{translator}
}

// Translate implements the SuffixTranslator interface.
// It extracts the base owner pool name using GetOwnerPoolName and delegates the translation to the wrapped translator.
func (s *suffixTranslatorWrapper) Translate(ownerPoolName string) string {
	return s.SuffixTranslator.Translate(GetOwnerPoolName(ownerPoolName))
}

// GetOwnerPoolName extracts the base owner pool name from a potentially wrapped name.
// It ignores the package name suffix if present.
func GetOwnerPoolName(ownerPoolName string) string {
	ownerPoolName, _ = UnwrapOwnerPoolName(ownerPoolName)
	return ownerPoolName
}

// GetResourcePackageConfig retrieves the configuration for a specific resource package on a NUMA node.
//
// Parameters:
//   - numaID: The ID of the NUMA node.
//   - pkgName: The name of the resource package.
//
// Returns:
//   - *ResourcePackageConfig: The configuration if found.
//   - error: An error if the receiver is nil, or if the NUMA ID or package name is not found.
func (r NUMAResourcePackageItems) GetResourcePackageConfig(numaID int, pkgName string) (*ResourcePackageConfig, error) {
	if r == nil {
		return nil, fmt.Errorf("numaResourcePackageItems is nil")
	}

	items, ok := r[numaID]
	if !ok {
		return nil, fmt.Errorf("numaID %d not found", numaID)
	}

	item, ok := items[pkgName]
	if !ok {
		return nil, fmt.Errorf("item not found for package %s on numa %d", pkgName, numaID)
	}

	return item.Config, nil
}

// GetPinnedCPUSetSize returns the size of the pinned CPU set for a specific resource package.
// It checks if the package is configured to use a pinned CPU set and if it has allocatable resources.
//
// Returns:
//   - *int: The size of the pinned CPU set (number of CPUs), or nil if not pinned/configured.
//   - error: An error if the item is not found or not properly configured.
func (r NUMAResourcePackageItems) GetPinnedCPUSetSize(numaID int, pkgName string) (*int, error) {
	if r == nil {
		return nil, fmt.Errorf("numaResourcePackageItems is nil")
	}

	items, ok := r[numaID]
	if !ok {
		return nil, fmt.Errorf("numaID %d not found", numaID)
	}

	item, ok := items[pkgName]
	if !ok {
		return nil, fmt.Errorf("item not found for package %s on numa %d", pkgName, numaID)
	}

	// Check if PinnedCPUSet is enabled in the config
	if item.Config == nil || item.Config.PinnedCPUSet == nil || !*item.Config.PinnedCPUSet {
		return nil, nil
	}

	if item.Allocatable == nil {
		return nil, fmt.Errorf("item not allocatable for package %s on numa %d", pkgName, numaID)
	}

	// Calculate size from allocatable CPU resources
	size := int(item.Allocatable.Cpu().Value())
	return &size, nil
}

// ListAllPinnedCPUSetSize lists the sizes of pinned CPU sets for all resource packages across all NUMA nodes.
// It filters out packages that do not have PinnedCPUSet enabled.
func (r NUMAResourcePackageItems) ListAllPinnedCPUSetSize() (map[int]map[string]int, error) {
	if r == nil {
		return nil, fmt.Errorf("numaResourcePackageItems is nil")
	}

	pinnedCPUSets := make(map[int]map[string]int)
	for numaID, items := range r {
		pinnedCPUSets[numaID] = make(map[string]int)
		for pkgName, item := range items {
			if item.Config == nil || item.Config.PinnedCPUSet == nil || !*item.Config.PinnedCPUSet {
				continue
			}

			if item.Allocatable == nil {
				continue
			}

			pinnedCPUSets[numaID][pkgName] = int(item.Allocatable.Cpu().Value())
		}
	}

	return pinnedCPUSets, nil
}

// GetAllPinnedCPUSetSizeSum calculates the total size of pinned CPU sets for each NUMA node.
// It sums up the CPU values of all pinned packages per NUMA node.
func (r NUMAResourcePackageItems) GetAllPinnedCPUSetSizeSum() (map[int]int, error) {
	if r == nil {
		return nil, fmt.Errorf("numaResourcePackageItems is nil")
	}

	pinnedCPUSets := make(map[int]int)
	for numaID, items := range r {
		size := int64(0)
		for _, item := range items {
			if item.Config == nil || item.Config.PinnedCPUSet == nil || !*item.Config.PinnedCPUSet {
				continue
			}

			if item.Allocatable == nil {
				continue
			}

			size += item.Allocatable.Cpu().Value()
		}
		pinnedCPUSets[numaID] = int(size)
	}

	return pinnedCPUSets, nil
}
