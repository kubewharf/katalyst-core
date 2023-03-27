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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cliflag "k8s.io/component-base/cli/flag"
	election "k8s.io/component-base/config"

	"github.com/kubewharf/katalyst-core/cmd/base/options"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

const (
	WorkModeProvider     = "provider"
	WorkModeCollector    = "collect"
	WorkModeStoreServing = "storeServer"
)

// Options holds the configurations for katalyst metrics module.
type Options struct {
	*options.GenericOptions

	election.LeaderElectionConfiguration

	WorkMode        []string
	OutOfDataPeriod time.Duration

	*StoreOptions
	*ProviderOptions
	*CollectorOptions
}

// NewOptions creates a new Options with a default config.
func NewOptions() *Options {
	return &Options{
		WorkMode:        []string{WorkModeProvider, WorkModeCollector},
		OutOfDataPeriod: time.Minute * 5,

		LeaderElectionConfiguration: election.LeaderElectionConfiguration{
			LeaderElect:       true,
			LeaseDuration:     metav1.Duration{Duration: 15 * time.Second},
			RenewDeadline:     metav1.Duration{Duration: 10 * time.Second},
			RetryPeriod:       metav1.Duration{Duration: 2 * time.Second},
			ResourceLock:      "leases",
			ResourceName:      "katalyst-custom-metric",
			ResourceNamespace: "kube-system",
		},

		GenericOptions:   options.NewGenericOptions(),
		StoreOptions:     NewStoreOptions(),
		ProviderOptions:  NewProviderOptions(),
		CollectorOptions: NewCollectorOptions(),
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *Options) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("metric")
	fs.StringSliceVar(&o.WorkMode, "work-mode", o.WorkMode, fmt.Sprintf("in which mode current process works on"))
	fs.DurationVar(&o.OutOfDataPeriod, "store-gc-period", o.OutOfDataPeriod, fmt.Sprintf(
		"how long we should keep the metric data as useful"))

	fs.BoolVar(&o.LeaderElect, "leader-elect", o.LeaderElect, ""+
		"Start a leader election client and gain leadership before "+
		"executing the main loop. Enable this when running replicated "+
		"components for high availability.")
	fs.DurationVar(&o.LeaseDuration.Duration, "leader-elect-lease-duration", o.LeaseDuration.Duration, ""+
		"The duration that non-leader candidates will wait after observing a leadership "+
		"renewal until attempting to acquire leadership of a led but unrenewed leader "+
		"slot. This is effectively the maximum duration that a leader can be stopped "+
		"before it is replaced by another candidate. This is only applicable if leader "+
		"election is enabled.")
	fs.DurationVar(&o.RenewDeadline.Duration, "leader-elect-renew-deadline", o.RenewDeadline.Duration, ""+
		"The interval between attempts by the acting master to renew a leadership slot "+
		"before it stops leading. This must be less than or equal to the lease duration. "+
		"This is only applicable if leader election is enabled.")
	fs.DurationVar(&o.RetryPeriod.Duration, "leader-elect-retry-period", o.RetryPeriod.Duration, ""+
		"The duration the clients should wait between attempting acquisition and renewal "+
		"of a leadership. This is only applicable if leader election is enabled.")
	fs.StringVar(&o.ResourceLock, "leader-elect-resource-lock", o.ResourceLock, ""+
		"The type of resource object that is used for locking during "+
		"leader election. Supported options are `endpoints` (default) and `configmaps`.")
	fs.StringVar(&o.ResourceName, "leader-elect-resource-name", o.ResourceName, ""+
		"The name of resource object that is used for locking during leader election.")
	fs.StringVar(&o.ResourceNamespace, "leader-elect-resource-namespace", o.ResourceNamespace, ""+
		"The namespace of resource object that is used for locking during leader election. ")

	o.GenericOptions.AddFlags(fss)

	o.StoreOptions.AddFlags(fss)
	o.ProviderOptions.AddFlags(fss)
	o.CollectorOptions.AddFlags(fss)
}

// ApplyTo fills up config with options
func (o *Options) ApplyTo(c *config.Configuration) error {
	c.GenericMetricConfiguration.OutOfDataPeriod = o.OutOfDataPeriod

	c.GenericMetricConfiguration.LeaderElection.LeaderElect = o.LeaderElect
	c.GenericMetricConfiguration.LeaderElection.LeaseDuration = o.LeaseDuration
	c.GenericMetricConfiguration.LeaderElection.RenewDeadline = o.RenewDeadline
	c.GenericMetricConfiguration.LeaderElection.RetryPeriod = o.RetryPeriod
	c.GenericMetricConfiguration.LeaderElection.ResourceLock = o.ResourceLock
	c.GenericMetricConfiguration.LeaderElection.ResourceName = o.ResourceName
	c.GenericMetricConfiguration.LeaderElection.ResourceNamespace = o.ResourceNamespace

	errList := make([]error, 0, 1)
	errList = append(errList, o.GenericOptions.ApplyTo(c.GenericConfiguration))

	c.WorkMode = o.WorkMode
	errList = append(errList, o.StoreOptions.ApplyTo(c.StoreConfiguration))
	errList = append(errList, o.ProviderOptions.ApplyTo(c.ProviderConfiguration))
	errList = append(errList, o.CollectorOptions.ApplyTo(c.CollectorConfiguration))
	return nil
}

func (o *Options) Config() (*config.Configuration, error) {
	c := config.NewConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
