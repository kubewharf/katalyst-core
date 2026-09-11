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

package filter

import "fmt"

var DefaultRegistry = NewRegistry()

type Registry map[string]AllocatableNICFilterFactory

func NewRegistry() Registry {
	return Registry{}
}

// Register registers a new allocatable NIC filter.
func (r Registry) Register(name string, factory AllocatableNICFilterFactory) error {
	if _, ok := r[name]; ok {
		return fmt.Errorf("a filter named %v already exists", name)
	}
	r[name] = factory
	return nil
}
