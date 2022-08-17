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

package general

import (
	"fmt"
	"sort"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

type ResourceList v1.ResourceList

func (r *ResourceList) String() string {
	var pairs []string
	for k, v := range *r {
		pairs = append(pairs, fmt.Sprintf("%s=%s", k, v.String()))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func (r *ResourceList) Set(value string) error {
	for _, s := range strings.Split(value, ",") {
		if len(s) == 0 {
			continue
		}
		arr := strings.SplitN(s, "=", 2)
		if len(arr) == 2 {
			parseQuantity, err := resource.ParseQuantity(arr[1])
			if err != nil {
				return err
			}
			(*r)[v1.ResourceName(strings.TrimSpace(arr[0]))] = parseQuantity
		}
	}
	return nil
}

func (r *ResourceList) Type() string {
	return "resourceList"
}
