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
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	componentbaseconfig "k8s.io/component-base/config"
	scheduleroptions "k8s.io/kubernetes/cmd/kube-scheduler/app/options"

	"github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions"
	"github.com/kubewharf/katalyst-core/cmd/base/options"
	schedulerappconfig "github.com/kubewharf/katalyst-core/cmd/katalyst-scheduler/app/config"
	"github.com/kubewharf/katalyst-core/pkg/client"
)

// Options has all the params needed to run a Scheduler
type Options struct {
	*scheduleroptions.Options
	*options.QoSOptions
}

// NewOptions returns default scheduler app options.
func NewOptions() *Options {
	return &Options{
		Options:    scheduleroptions.NewOptions(),
		QoSOptions: options.NewQoSOptions(),
	}
}

// Config return a scheduler config object
func (o *Options) Config() (*schedulerappconfig.Config, *generic.QoSConfiguration, error) {
	config, err := o.Options.Config()
	if err != nil {
		return nil, nil, err
	}

	qosConfig := generic.NewQoSConfiguration()
	if o.QoSOptions.ApplyTo(qosConfig); err != nil {
		return nil, nil, err
	}

	kubeConfig, err := createKubeConfig(config.ComponentConfig.ClientConnection, "")
	if err != nil {
		return nil, nil, err
	}

	clientSet := client.NewGenericClientWithName("katalyst-scheduler", kubeConfig)
	internalInformerFactory := externalversions.NewSharedInformerFactory(clientSet.InternalClient, 0)

	return &schedulerappconfig.Config{
		Config:                  config,
		InternalClient:          clientSet.InternalClient,
		InternalInformerFactory: internalInformerFactory,
	}, qosConfig, nil
}

// createKubeConfig creates a kubeConfig from the given config and masterOverride.
func createKubeConfig(config componentbaseconfig.ClientConnectionConfiguration, masterOverride string) (*restclient.Config, error) {
	kubeConfig, err := clientcmd.BuildConfigFromFlags(masterOverride, config.Kubeconfig)
	if err != nil {
		return nil, err
	}

	kubeConfig.DisableCompression = true
	kubeConfig.AcceptContentTypes = config.AcceptContentTypes
	kubeConfig.ContentType = config.ContentType
	kubeConfig.QPS = config.QPS
	kubeConfig.Burst = int(config.Burst)

	return kubeConfig, nil
}
