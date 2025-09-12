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
	// HasPreStop indicates whether there is a pre-stop script in the daemonset manifest of QRM
	// This is important because if we do not have a pre-stop script, we cannot proceed with migration of state file
	// because in the case of rollback, we need the pre-stop script to ensure that the StateFileDirectory and InMemoryStateFileDirectory
	// are synchronized.
	HasPreStop bool
}

func NewStateDirectoryConfiguration() *StateDirectoryConfiguration {
	return &StateDirectoryConfiguration{}
}

func (c *StateDirectoryConfiguration) GetCurrentAndOtherStateFileDirectory() (string, string) {
	if !c.HasPreStop {
		// At the time of development, we always use StateFileDirectory to store state file.
		// In case we do not have a pre-stop script, the current directory will always be StateFileDirectory
		return c.StateFileDirectory, c.InMemoryStateFileDirectory
	}
	if c.EnableInMemoryState {
		return c.InMemoryStateFileDirectory, c.StateFileDirectory
	}
	return c.StateFileDirectory, c.InMemoryStateFileDirectory
}
