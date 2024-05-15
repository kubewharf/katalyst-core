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
	"flag"
	"os"

	"k8s.io/apimachinery/pkg/util/errors"
	cliflag "k8s.io/component-base/cli/flag"
	componentbaseconfig "k8s.io/component-base/config"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

// GenericOptions holds the configurations for multi components.
type GenericOptions struct {
	DryRun             bool
	EnableHealthzCheck bool

	MasterURL  string
	KubeConfig string

	TransformedInformerForPod bool

	// todo actually those auth info should be stored in secrets or somewhere like that
	GenericEndpoint             string
	GenericEndpointHandleChains []string

	qosOptions     *QoSOptions
	metricsOptions *MetricsOptions
	logsOptions    *LogsOptions
	authOptions    *AuthOptions

	componentbaseconfig.ClientConnectionConfiguration
}

func NewGenericOptions() *GenericOptions {
	return &GenericOptions{
		DryRun:                    false,
		EnableHealthzCheck:        false,
		TransformedInformerForPod: false,
		GenericEndpoint:           ":9316",
		qosOptions:                NewQoSOptions(),
		metricsOptions:            NewMetricsOptions(),
		logsOptions:               NewLogsOptions(),
		authOptions:               NewAuthOptions(),
		GenericEndpointHandleChains: []string{
			process.HTTPChainCredential, process.HTTPChainRateLimiter,
			process.HTTPChainMonitor,
		},
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *GenericOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("generic")

	local := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	klog.InitFlags(local)
	local.VisitAll(func(fl *flag.Flag) {
		fs.AddGoFlag(fl)
	})

	fs.BoolVar(&o.DryRun, "dry-run", o.DryRun, "A bool to enable and disable dry-run.")
	fs.BoolVar(&o.EnableHealthzCheck, "enable-healthz-check", o.EnableHealthzCheck, "A bool to enable and disable healthz check.")

	fs.BoolVar(&o.TransformedInformerForPod, "transformed-informer-pod", o.TransformedInformerForPod,
		"whether we should enable the ability of transformed informer for pods")

	fs.StringVar(&o.MasterURL, "master", o.MasterURL,
		`The url of the Kubernetes API server, will overrides any value in kubeconfig, only required if out-of-cluster.`)
	fs.StringVar(&o.KubeConfig, "kubeconfig", o.KubeConfig, "The path of kubeconfig file")

	fs.StringVar(&o.GenericEndpoint, "generic-endpoint", o.GenericEndpoint,
		"the endpoint of generic purpose, which will use as prometheus, health check and profiling")
	fs.StringSliceVar(&o.GenericEndpointHandleChains, "generic-handler-chains", o.GenericEndpointHandleChains,
		"this flag defines the handler chains that should be enabled")

	o.qosOptions.AddFlags(fs)
	o.metricsOptions.AddFlags(fs)
	o.logsOptions.AddFlags(fs)
	o.authOptions.AddFlags(fs)

	fs.Float32Var(&o.QPS, "kube-api-qps", o.QPS, "QPS to use while talking with kubernetes apiserver.")
	fs.Int32Var(&o.Burst, "kube-api-burst", o.Burst, "Burst to use while talking with kubernetes apiserver.")
}

// ApplyTo fills up config with options
func (o *GenericOptions) ApplyTo(c *generic.GenericConfiguration) error {
	c.DryRun = o.DryRun
	c.EnableHealthzCheck = o.EnableHealthzCheck

	c.TransformedInformerForPod = o.TransformedInformerForPod

	c.GenericEndpoint = o.GenericEndpoint
	c.GenericEndpointHandleChains = o.GenericEndpointHandleChains

	errList := make([]error, 0, 1)
	errList = append(errList, o.qosOptions.ApplyTo(c.QoSConfiguration))
	errList = append(errList, o.metricsOptions.ApplyTo(c.MetricsConfiguration))
	errList = append(errList, o.logsOptions.ApplyTo())
	errList = append(errList, o.authOptions.ApplyTo(c.AuthConfiguration))

	c.ClientConnection.QPS = o.QPS
	c.ClientConnection.Burst = o.Burst

	return errors.NewAggregate(errList)
}
