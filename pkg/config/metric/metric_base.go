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

package metric

import (
	componentbaseconfig "k8s.io/component-base/config"
)

type GenericMetricConfiguration struct {
	// ClientConnection specifies the kubeconfig file and client connection
	// settings for the proxy server to use when communicating with the apiserver.
	ClientConnection componentbaseconfig.ClientConnectionConfiguration

	// leaderElection defines the configuration of leader election client.
	LeaderElection componentbaseconfig.LeaderElectionConfiguration
}

type CustomMetricConfiguration struct {
	WorkMode []string

	*CollectorConfiguration
	*StoreConfiguration
	*ProviderConfiguration
}

func NewGenericMetricConfiguration() *GenericMetricConfiguration {
	return &GenericMetricConfiguration{}
}

func NewCustomMetricConfiguration() *CustomMetricConfiguration {
	return &CustomMetricConfiguration{
		CollectorConfiguration: NewCollectorConfiguration(),
		StoreConfiguration:     NewStoreConfiguration(),
		ProviderConfiguration:  NewProviderConfiguration(),
	}
}
