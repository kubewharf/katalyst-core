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

import (
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/statedirectory"
)

type StateDirectoryOptions struct {
	StateFileDirectory         string
	InMemoryStateFileDirectory string
	EnableInMemoryState        bool
	HasPreStop                 bool
}

func NewStateDirectoryOptions() *StateDirectoryOptions {
	return &StateDirectoryOptions{
		StateFileDirectory:         "/var/lib/katalyst/qrm_advisor",
		InMemoryStateFileDirectory: "/dev/shm/qrm/state",
	}
}

func (o *StateDirectoryOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("qrm_state_directory")

	fs.StringVar(&o.StateFileDirectory, "qrm-state-dir", o.StateFileDirectory, "The directory to store the state file.")
	fs.StringVar(&o.InMemoryStateFileDirectory, "qrm-state-dir-in-memory",
		o.InMemoryStateFileDirectory, "The in memory directory to store the state file.")
	fs.BoolVar(&o.EnableInMemoryState, "qrm-enable-in-memory-state",
		o.EnableInMemoryState, "if set true, the state will be stored in the in-memory directory.")
	fs.BoolVar(&o.HasPreStop, "qrm-has-pre-stop",
		o.HasPreStop, "if set true, there is a pre-stop script in place.")
}

func (o *StateDirectoryOptions) ApplyTo(conf *statedirectory.StateDirectoryConfiguration) error {
	conf.StateFileDirectory = o.StateFileDirectory
	conf.InMemoryStateFileDirectory = o.InMemoryStateFileDirectory
	conf.EnableInMemoryState = o.EnableInMemoryState
	conf.HasPreStop = o.HasPreStop
	return nil
}
