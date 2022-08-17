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
	componentbaseconfig "k8s.io/component-base/config"
	election "k8s.io/component-base/config"

	controllerconfig "github.com/kubewharf/katalyst-core/pkg/config/controller"
)

// GenericControllerOptions holds the configurations for controller based configurations.
type GenericControllerOptions struct {
	Controllers        []string
	LabelSelector      string
	DryRun             bool
	DynamicGVResources []string

	election.LeaderElectionConfiguration
	componentbaseconfig.ClientConnectionConfiguration
}

// NewGenericControllerOptions creates a new Options with a default config.
func NewGenericControllerOptions() *GenericControllerOptions {
	return &GenericControllerOptions{
		DryRun: true,
		LeaderElectionConfiguration: election.LeaderElectionConfiguration{
			LeaderElect:       true,
			LeaseDuration:     metav1.Duration{Duration: 15 * time.Second},
			RenewDeadline:     metav1.Duration{Duration: 10 * time.Second},
			RetryPeriod:       metav1.Duration{Duration: 2 * time.Second},
			ResourceLock:      "leases",
			ResourceName:      "katalyst-controller",
			ResourceNamespace: "kube-system",
		},
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *GenericControllerOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("generic-controller")

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

	fs.StringSliceVar(&o.Controllers, "controllers", o.Controllers, fmt.Sprintf(""+
		"A list of controllers to enable. '*' enables all on-by-default controllers, 'foo' enables the controller "+
		"named 'foo', '-foo' disables the controller named 'foo'"))

	fs.StringVar(&o.LabelSelector, "label-selector", o.LabelSelector, fmt.Sprintf(""+
		"A selector to restrict the list of returned objects by their labels. this selector is used in informer factory."))

	fs.BoolVar(&o.DryRun, "dry-run", o.DryRun, "A bool to enable and disable dry-run.")

	fs.Float32Var(&o.QPS, "kube-api-qps", o.QPS, "QPS to use while talking with kubernetes apiserver.")
	fs.Int32Var(&o.Burst, "kube-api-burst", o.Burst, "Burst to use while talking with kubernetes apiserver.")

	fs.StringSliceVar(&o.DynamicGVResources, "dynamic-resources", o.DynamicGVResources, fmt.Sprintf(""+
		"A list of resources to be list and watched. "+
		"DynamicGVResources should be in the format of `resource.version.group.com` like 'deployments.v1.apps'."))
}

// ApplyTo fills up config with options
func (o *GenericControllerOptions) ApplyTo(c *controllerconfig.GenericControllerConfiguration) error {
	c.LeaderElection.LeaderElect = o.LeaderElect
	c.LeaderElection.LeaseDuration = o.LeaseDuration
	c.LeaderElection.RenewDeadline = o.RenewDeadline
	c.LeaderElection.RetryPeriod = o.RetryPeriod
	c.LeaderElection.ResourceLock = o.ResourceLock
	c.LeaderElection.ResourceName = o.ResourceName
	c.LeaderElection.ResourceNamespace = o.ResourceNamespace

	c.ClientConnection.QPS = o.QPS
	c.ClientConnection.Burst = o.Burst

	c.DryRun = o.DryRun
	c.Controllers = o.Controllers
	c.LabelSelector = o.LabelSelector
	c.DynamicGVResources = o.DynamicGVResources

	return nil
}

func (o *GenericControllerOptions) Config() (*controllerconfig.GenericControllerConfiguration, error) {
	c := controllerconfig.NewGenericControllerConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
