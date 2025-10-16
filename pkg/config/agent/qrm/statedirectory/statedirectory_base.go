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

package statedirectory

type StateDirectoryConfiguration struct {
	StateFileDirectory         string
	InMemoryStateFileDirectory string
	// EnableInMemoryState indicates whether we want to store the state in memory or on disk
	// if set true, the state will be stored in tmpfs
	EnableInMemoryState bool
	// HasPreStop indicates whether we have pre-stop script in place
	HasPreStop bool
}

func NewStateDirectoryConfiguration() *StateDirectoryConfiguration {
	return &StateDirectoryConfiguration{}
}

func (c *StateDirectoryConfiguration) GetCurrentAndPreviousStateFileDirectory() (string, string) {
	if !c.HasPreStop {
		return c.StateFileDirectory, c.InMemoryStateFileDirectory
	}
	if c.EnableInMemoryState {
		return c.InMemoryStateFileDirectory, c.StateFileDirectory
	}
	return c.StateFileDirectory, c.InMemoryStateFileDirectory
}
