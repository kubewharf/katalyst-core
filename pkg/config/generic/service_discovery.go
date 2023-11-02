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

package generic

import "k8s.io/apimachinery/pkg/labels"

type PodSinglePortSDConf struct {
	PortName  string
	PodLister labels.Selector
}

func NewPodSingleSDConf() *PodSinglePortSDConf {
	return &PodSinglePortSDConf{}
}

type ServiceSinglePortSDConf struct {
	Namespace string
	Name      string
	PortName  string
}

func NewServiceSinglePortSDConf() *ServiceSinglePortSDConf {
	return &ServiceSinglePortSDConf{}
}

type ServiceDiscoveryConf struct {
	Name string

	*PodSinglePortSDConf
	*ServiceSinglePortSDConf
}

func NewServiceDiscoveryConf() *ServiceDiscoveryConf {
	return &ServiceDiscoveryConf{
		PodSinglePortSDConf:     NewPodSingleSDConf(),
		ServiceSinglePortSDConf: NewServiceSinglePortSDConf(),
	}
}
