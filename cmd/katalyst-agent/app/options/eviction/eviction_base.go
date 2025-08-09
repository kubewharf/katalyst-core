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
	"time"

	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"

	evictionconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/eviction"
	"github.com/kubewharf/katalyst-core/pkg/consts"
)

// GenericEvictionOptions holds the configurations for eviction manager.
type GenericEvictionOptions struct {
	InnerPlugins []string

	// ConditionTransitionPeriod is duration the eviction manager has to wait before transitioning out of a condition.
	ConditionTransitionPeriod time.Duration

	// EvictionManagerSyncPeriod is the interval duration that eviction manager fetches information from registered plugins
	EvictionManagerSyncPeriod time.Duration

	// those two variables are used to filter out eviction-free pods
	EvictionSkippedAnnotationKeys []string
	EvictionSkippedLabelKeys      []string

	// EvictionBurst limit the burst eviction counts
	EvictionBurst int

	// PodKiller specify the pod killer implementation
	PodKiller string

	// StrictAuthentication means whether to authenticate plugins strictly
	StrictAuthentication bool

	// PodMetricLabels defines the pod labels to be added into metric selector list.
	PodMetricLabels []string

	// HostPathNotifierPathRoot is the root path for host-path notifier
	HostPathNotifierRootPath string
}

// NewGenericEvictionOptions creates a new Options with a default config.
func NewGenericEvictionOptions() *GenericEvictionOptions {
	return &GenericEvictionOptions{
		InnerPlugins:                  []string{},
		ConditionTransitionPeriod:     5 * time.Minute,
		EvictionManagerSyncPeriod:     5 * time.Second,
		EvictionSkippedAnnotationKeys: []string{},
		EvictionSkippedLabelKeys:      []string{},
		EvictionBurst:                 3,
		HostPathNotifierRootPath:      "/opt/katalyst",
		PodKiller:                     consts.KillerNameEvictionKiller,
		StrictAuthentication:          false,
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *GenericEvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("eviction")

	fs.StringSliceVar(&o.InnerPlugins, "eviction-plugins", o.InnerPlugins, fmt.Sprintf(""+
		"A list of eviction plugins to enable. '*' enables all on-by-default eviction plugins, 'foo' enables the eviction plugin "+
		"named 'foo', '-foo' disables the eviction plugin named 'foo'"))

	fs.DurationVar(&o.ConditionTransitionPeriod, "eviction-condition-transition-period", o.ConditionTransitionPeriod,
		"duration the eviction manager has to wait before transitioning out of a condition")

	fs.DurationVar(&o.EvictionManagerSyncPeriod, "eviction-manager-sync-period", o.EvictionManagerSyncPeriod,
		"interval duration that eviction manager fetches information from registered plugins")

	fs.StringSliceVar(&o.EvictionSkippedAnnotationKeys, "eviction-skipped-annotation", o.EvictionSkippedAnnotationKeys,
		"A list of annotations to identify a bunch of pods that should be filtered out during eviction")
	fs.StringSliceVar(&o.EvictionSkippedLabelKeys, "eviction-skipped-labels", o.EvictionSkippedLabelKeys,
		"A list of labels to identify a bunch of pods that should be filtered out during eviction")

	fs.IntVar(&o.EvictionBurst, "eviction-burst", o.EvictionBurst,
		"The burst amount of pods to be evicted by edition manager")

	fs.StringVar(&o.PodKiller, "pod-killer", o.PodKiller,
		"the pod killer used to evict pod")

	fs.BoolVar(&o.StrictAuthentication, "strict-authentication", o.StrictAuthentication,
		"whether to authenticate plugins strictly, the out-of-tree plugins must use valid and authorized token "+
			"to register if it set to true")

	fs.StringSliceVar(&o.PodMetricLabels, "eviction-pod-metric-labels", o.PodMetricLabels,
		"The pod labels to be added into metric selector list")

	fs.StringVar(&o.HostPathNotifierRootPath, "pod-notifier-root-path", o.HostPathNotifierRootPath,
		"root path of host-path notifier")
}

// ApplyTo fills up config with options
func (o *GenericEvictionOptions) ApplyTo(c *evictionconfig.GenericEvictionConfiguration) error {
	c.InnerPlugins = o.InnerPlugins
	c.ConditionTransitionPeriod = o.ConditionTransitionPeriod
	c.EvictionManagerSyncPeriod = o.EvictionManagerSyncPeriod
	c.EvictionSkippedAnnotationKeys.Insert(o.EvictionSkippedAnnotationKeys...)
	c.EvictionSkippedLabelKeys.Insert(o.EvictionSkippedLabelKeys...)
	c.EvictionBurst = o.EvictionBurst
	c.PodKiller = o.PodKiller
	c.StrictAuthentication = o.StrictAuthentication
	c.PodMetricLabels.Insert(o.PodMetricLabels...)
	c.HostPathNotifierRootPath = o.HostPathNotifierRootPath
	return nil
}

func (o *GenericEvictionOptions) Config() (*evictionconfig.GenericEvictionConfiguration, error) {
	c := evictionconfig.NewGenericEvictionConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}

type EvictionOptions struct {
	*ReclaimedResourcesEvictionOptions
	*MemoryPressureEvictionOptions
	*CPUPressureEvictionOptions
}

func NewEvictionOptions() *EvictionOptions {
	return &EvictionOptions{
		ReclaimedResourcesEvictionOptions: NewReclaimedResourcesEvictionOptions(),
		MemoryPressureEvictionOptions:     NewMemoryPressureEvictionOptions(),
		CPUPressureEvictionOptions:        NewCPUPressureEvictionOptions(),
	}
}

func (o *EvictionOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	o.ReclaimedResourcesEvictionOptions.AddFlags(fss)
	o.MemoryPressureEvictionOptions.AddFlags(fss)
	o.CPUPressureEvictionOptions.AddFlags(fss)
}

// ApplyTo fills up config with options
func (o *EvictionOptions) ApplyTo(c *evictionconfig.EvictionConfiguration) error {
	var errList []error
	errList = append(errList,
		o.ReclaimedResourcesEvictionOptions.ApplyTo(c.ReclaimedResourcesEvictionConfiguration),
		o.MemoryPressureEvictionOptions.ApplyTo(c.MemoryPressureEvictionConfiguration),
		o.CPUPressureEvictionOptions.ApplyTo(c.CPUPressureEvictionConfiguration),
	)
	return errors.NewAggregate(errList)
}

func (o *EvictionOptions) Config() (*evictionconfig.EvictionConfiguration, error) {
	c := evictionconfig.NewEvictionConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
