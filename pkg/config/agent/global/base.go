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

package global

import "github.com/kubewharf/katalyst-core/pkg/config/dynamic"

type BaseConfiguration struct {
	// Agents is the list of agent components to enable or disable
	// '*' means "all enabled by default agents"
	// 'foo' means "enable 'foo'"
	// '-foo' means "disable 'foo'"
	// first item for a particular name wins
	Agents []string

	NodeName string

	// LockFileName indicates the file used as unique lock
	LockFileName string
}

func NewBaseConfiguration() *BaseConfiguration {
	return &BaseConfiguration{}
}

func (c *BaseConfiguration) ApplyConfiguration(*BaseConfiguration, *dynamic.DynamicConfigCRD) {
}
