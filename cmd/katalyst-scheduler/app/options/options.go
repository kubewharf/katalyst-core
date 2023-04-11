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

	scheduleroptions "k8s.io/kubernetes/cmd/kube-scheduler/app/options"

	"github.com/kubewharf/katalyst-api/pkg/client/informers/externalversions"
	"github.com/kubewharf/katalyst-core/cmd/base/options"
	schedulerappconfig "github.com/kubewharf/katalyst-core/cmd/katalyst-scheduler/app/config"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/consts"
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
	if err = o.QoSOptions.ApplyTo(qosConfig); err != nil {
		return nil, nil, err
	}

	clientSet, err := client.BuildGenericClient(config.ComponentConfig.ClientConnection, "",
		"", fmt.Sprintf("%v", consts.KatalystComponentScheduler))
	if err != nil {
		return nil, nil, err
	}

	internalInformerFactory := externalversions.NewSharedInformerFactory(clientSet.InternalClient, 0)
	return &schedulerappconfig.Config{
		Config:                  config,
		InternalClient:          clientSet.InternalClient,
		InternalInformerFactory: internalInformerFactory,
	}, qosConfig, nil
}
