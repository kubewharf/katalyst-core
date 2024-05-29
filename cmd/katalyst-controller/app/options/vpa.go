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

package options

import (
	"fmt"
	"time"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/controller"
)

const (
	defaultVpaSyncWorkers                   = 1
	defaultVpaRecSyncWorkers                = 1
	defaultResourceRecommendResyncVPAPeriod = 30 * time.Second
)

// VPARecommendationOptions holds the configurations for vertical pod auto-scaler recommendation.
type VPARecommendationOptions struct{}

// ResourceRecommendOptions holds the configurations for resource recommend.
type ResourceRecommendOptions struct {
	// time interval of resync VPA
	VPAResyncPeriod time.Duration
}

// VPAOptions holds the configurations for vertical pod auto-scaler.
type VPAOptions struct {
	// we use VPAWorkloadGVResources to define those VPA concerned GVRs
	VPAWorkloadGVResources []string
	VPAPodLabelIndexerKeys []string
	// count of workers to sync VPA and VPARec
	VPASyncWorkers    int
	VPARecSyncWorkers int

	VPARecommendationOptions
	ResourceRecommendOptions
}

// NewVPAOptions creates a new Options with a default config.
func NewVPAOptions() *VPAOptions {
	return &VPAOptions{}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *VPAOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("vpa")

	fs.StringSliceVar(&o.VPAWorkloadGVResources, "vpa-workload-resources", o.VPAWorkloadGVResources, fmt.Sprintf(""+
		"A list of resources to be vpa controller watched. "+
		"VPAWorkloadGVResources should be in the format of `resource.version.group.com` like 'deployments.v1.apps'."))
	fs.StringSliceVar(&o.VPAPodLabelIndexerKeys, "vpa-pod-label-indexers", o.VPAPodLabelIndexerKeys, ""+
		"A list of pod label keys to be used as indexers for pod informer")
	fs.IntVar(&o.VPASyncWorkers, "vpa-sync-workers", defaultVpaSyncWorkers, "num of goroutines to sync vpas")
	fs.IntVar(&o.VPARecSyncWorkers, "vparec-sync-workers", defaultVpaRecSyncWorkers, "num of goroutines to sync vparecs")
	fs.DurationVar(&o.ResourceRecommendOptions.VPAResyncPeriod, "resource-recommend-resync-vpa-period",
		defaultResourceRecommendResyncVPAPeriod, "Period for recommend controller to sync vpa")
}

// ApplyTo fills up config with options
func (o *VPAOptions) ApplyTo(c *controller.VPAConfig) error {
	c.VPAWorkloadGVResources = o.VPAWorkloadGVResources
	c.VPAPodLabelIndexerKeys = o.VPAPodLabelIndexerKeys
	c.VPASyncWorkers = o.VPASyncWorkers
	c.VPARecSyncWorkers = o.VPARecSyncWorkers
	c.ResourceRecommendConfig.VPAReSyncPeriod = o.ResourceRecommendOptions.VPAResyncPeriod
	return nil
}

func (o *VPAOptions) Config() (*controller.VPAConfig, error) {
	c := &controller.VPAConfig{}
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
