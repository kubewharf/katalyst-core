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

package controller

import (
	componentbaseconfig "k8s.io/component-base/config"
)

type GenericControllerConfiguration struct {
	// Controllers is the list of controllers to enable or disable
	// '*' means "all enabled by default controllers"
	// 'foo' means "enable 'foo'"
	// '-foo' means "disable 'foo'"
	// first item for a particular name wins
	Controllers []string

	// leaderElection defines the configuration of leader election client.
	LeaderElection componentbaseconfig.LeaderElectionConfiguration

	// A selector to restrict the list of returned objects by their labels. this selector is
	// used in informer factory.
	LabelSelector string

	// since many centralized components need a dynamic mechanism to
	// list-watch or get GYR from APIServer (such as autoscaling and
	// service-profiling), we use DynamicGVResources to define those GVRs
	DynamicGVResources []string
}

type ControllersConfiguration struct {
	*IHPAConfig
	*VPAConfig
	*KCCConfig
	*SPDConfig
	*LifeCycleConfig
	*MonitorConfig
	*OvercommitConfig
	*TideConfig
	*ResourceRecommenderConfig
}

func NewGenericControllerConfiguration() *GenericControllerConfiguration {
	return &GenericControllerConfiguration{}
}

func NewControllersConfiguration() *ControllersConfiguration {
	return &ControllersConfiguration{
		IHPAConfig:                NewIHPAConfig(),
		VPAConfig:                 NewVPAConfig(),
		KCCConfig:                 NewKCCConfig(),
		SPDConfig:                 NewSPDConfig(),
		LifeCycleConfig:           NewLifeCycleConfig(),
		MonitorConfig:             NewMonitorConfig(),
		OvercommitConfig:          NewOvercommitConfig(),
		TideConfig:                NewTideConfig(),
		ResourceRecommenderConfig: NewResourceRecommenderConfig(),
	}
}
