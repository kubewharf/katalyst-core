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

package resourcepool

import "github.com/kubewharf/katalyst-api/pkg/consts"

// GetResourcePoolName retrieves the resource pool name from pod annotations.
// It looks for the key "katalyst.kubewharf.io/resource_pool" in the annotations map.
//
// Parameters:
//   - annotations: A map of pod annotations where to look for the resource pool name.
//
// Returns:
//   - string: The resource pool name if found, otherwise an empty string.
func GetResourcePoolName(annotations map[string]string) string {
	poolName, ok := annotations[consts.PodAnnotationResourcePoolKey]
	if !ok {
		return ""
	}

	return poolName
}
