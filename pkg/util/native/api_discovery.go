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
	"errors"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
)

type apiResourceNotFoundError struct {
	resource      string
	groupVersions []string
}

func (e *apiResourceNotFoundError) Error() string {
	return fmt.Sprintf(
		"API resource %q is not served in group versions %v",
		e.resource, e.groupVersions)
}

func IsAPIResourceNotFound(err error) bool {
	var target *apiResourceNotFoundError
	return errors.As(err, &target)
}

func FindSupportedAPIResource(
	client discovery.DiscoveryInterface,
	resource string,
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
			if apiResource.Name != resource {
				continue
			}
			return groupVersion, &apiResource, nil
		}
	}

	return "", nil, &apiResourceNotFoundError{
		resource:      resource,
		groupVersions: groupVersions,
	}
}

func FindSupportedGroupVersion(
	client discovery.DiscoveryInterface,
	resource string,
	groupVersions ...string,
) (string, error) {
	groupVersion, _, err := FindSupportedAPIResource(client, resource, groupVersions...)
	return groupVersion, err
}
