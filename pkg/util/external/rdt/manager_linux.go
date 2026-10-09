//go:build linux
// +build linux

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

package rdt

import (
	"errors"

	"github.com/kubewharf/katalyst-core/pkg/consts"
)

type defaultRDTManager struct {
	schemataCoordinator *SchemataCoordinator
}

var defaultSchemataCoordinator = newSchemataCoordinator(consts.DefaultResctrlRootDir)

// NewDefaultManager returns a defaultRDTManager.
func NewDefaultManager() RDTManager {
	return &defaultRDTManager{
		schemataCoordinator: defaultSchemataCoordinator,
	}
}

// CheckSupportRDT checks whether RDT is supported by the CPU and the kernel.
func (*defaultRDTManager) CheckSupportRDT() (bool, error) {
	// TODO: implement CheckSupportRDT
	return false, errors.New("not implemented yet")
}

// InitRDT performs some RDT-related initializations.
func (*defaultRDTManager) InitRDT() error {
	// TODO: implement InitRDT
	return errors.New("not implemented yet")
}

// ApplyTasks synchronizes the tasks of each CLOS.
func (*defaultRDTManager) ApplyTasks(clos string, tasks []string) error {
	// TODO: implement ApplyTasks
	return errors.New("not implemented yet")
}

// ApplyCAT applies only the L3 schemata configuration for a CLOS.
func (m *defaultRDTManager) ApplyCAT(clos string, cat map[int]uint64) error {
	return m.schemataCoordinator.ApplyL3(clos, cat)
}

// ApplyMBA applies only the MB schemata configuration for a CLOS.
func (m *defaultRDTManager) ApplyMBA(clos string, mba map[int]int) error {
	return m.schemataCoordinator.ApplyMB(clos, mba)
}

// InvalidateClos clears cached schemata targets for a recreated or removed CLOS.
func (m *defaultRDTManager) InvalidateClos(clos string) {
	m.schemataCoordinator.InvalidateClos(clos)
}

// RunClosResourceUpdate serializes a non-schemata CLOS resource update with
// schemata updates and lifecycle transitions for the same CLOS.
func (m *defaultRDTManager) RunClosResourceUpdate(clos string, update func() (bool, error)) error {
	return m.schemataCoordinator.RunClosResourceUpdate(clos, update)
}

// RunClosLifecycle serializes a CLOS lifecycle update with schemata updates.
func (m *defaultRDTManager) RunClosLifecycle(clos string, update func() (bool, error)) error {
	return m.schemataCoordinator.RunClosLifecycle(clos, update)
}
