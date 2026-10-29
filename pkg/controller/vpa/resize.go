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

package vpa

import (
	"fmt"

	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/discovery"
)

// validatePodResizeSupport checks if the native pod resize API is supported.
// If it is supported, we return an error as VPA is not implemented for this feature.
func validatePodResizeSupport(client discovery.DiscoveryInterface) error {
	resources, err := client.ServerResourcesForGroupVersion(core.SchemeGroupVersion.String())
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("discover core v1 resources: %w", err)
	}

	for _, apiResource := range resources.APIResources {
		if apiResource.Name != "pods/resize" {
			continue
		}
		for _, verb := range apiResource.Verbs {
			if verb == "update" {
				return fmt.Errorf("VPA is not implemented for native pod resize")
			}
		}
	}
	return nil
}
