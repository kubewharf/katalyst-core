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

package eviction

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/config/dynamic"
)

// ResourceEvictionThreshold is map of resource name to rate of eviction water level
type ResourceEvictionThreshold map[v1.ResourceName]float64

func (t *ResourceEvictionThreshold) Type() string {
	return "evictionThreshold"
}

func (t *ResourceEvictionThreshold) String() string {
	var pairs []string
	for k, v := range *t {
		pairs = append(pairs, fmt.Sprintf("%s=%f", k, v))
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

func (t *ResourceEvictionThreshold) Set(value string) error {
	for _, s := range strings.Split(value, ",") {
		if len(s) == 0 {
			continue
		}
		arr := strings.SplitN(s, "=", 2)
		if len(arr) == 2 {
			parseFloat, err := strconv.ParseFloat(arr[1], 64)
			if err != nil {
				return err
			}
			(*t)[v1.ResourceName(strings.TrimSpace(arr[0]))] = parseFloat
		}
	}
	return nil
}

type ReclaimedResourcesEvictionPluginConfiguration struct {
	EvictionReclaimedPodGracefulPeriod int64
	EvictionThreshold                  ResourceEvictionThreshold
	SkipZeroQuantityResourceNames      sets.String
}

func NewReclaimedResourcesEvictionPluginConfiguration() *ReclaimedResourcesEvictionPluginConfiguration {
	return &ReclaimedResourcesEvictionPluginConfiguration{
		EvictionThreshold:             ResourceEvictionThreshold{},
		SkipZeroQuantityResourceNames: sets.String{},
	}
}

func (c *ReclaimedResourcesEvictionPluginConfiguration) ApplyConfiguration(conf *dynamic.DynamicConfigCRD) {
	if ec := conf.EvictionConfiguration; ec != nil {
		for resourceName, value := range ec.Spec.Config.EvictionPluginsConfig.ReclaimedResourcesEvictionPluginConfig.EvictionThreshold {
			c.EvictionThreshold[resourceName] = value
		}
	}
}
