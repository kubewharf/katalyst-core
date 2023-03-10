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

// GenericConfiguration stores all the generic configurations needed
// by core katalyst components
type GenericConfiguration struct {
	GenericEndpoint             string
	GenericEndpointHandleChains []string
	GenericAuthStaticUser       string
	GenericAuthStaticPasswd     string

	*QoSConfiguration
	*MetricsConfiguration
}

// NewGenericConfiguration creates a new generic configuration.
func NewGenericConfiguration() *GenericConfiguration {
	return &GenericConfiguration{
		QoSConfiguration:     NewQoSConfiguration(),
		MetricsConfiguration: NewMetricsConfiguration(),
	}
}
