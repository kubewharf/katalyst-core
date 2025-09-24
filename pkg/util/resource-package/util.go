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

import "github.com/kubewharf/katalyst-api/pkg/consts"

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
