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

package native

import (
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
)

func FindSupportedAPIResource(
	client discovery.DiscoveryInterface,
	resource string,
	requiredVerbs []string,
	groupVersions ...string,
) (string, *metav1.APIResource, error) {
	for _, groupVersion := range groupVersions {
		resourceList, err := client.ServerResourcesForGroupVersion(groupVersion)
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return "", nil, fmt.Errorf("discover API resources for %s: %w", groupVersion, err)
		}

		for i := range resourceList.APIResources {
			apiResource := resourceList.APIResources[i]
			if apiResource.Name != resource || !hasAllVerbs(apiResource.Verbs, requiredVerbs) {
				continue
			}
			return groupVersion, &apiResource, nil
		}
	}

	return "", nil, fmt.Errorf(
		"API resource %q with verbs %v is not served in group versions %v",
		resource, requiredVerbs, groupVersions)
}

func FindSupportedGroupVersion(
	client discovery.DiscoveryInterface,
	resource string,
	requiredVerbs []string,
	groupVersions ...string,
) (string, error) {
	groupVersion, _, err := FindSupportedAPIResource(client, resource, requiredVerbs, groupVersions...)
	return groupVersion, err
}

func hasAllVerbs(actual metav1.Verbs, required []string) bool {
	for _, requiredVerb := range required {
		found := false
		for _, actualVerb := range actual {
			if actualVerb == requiredVerb {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
