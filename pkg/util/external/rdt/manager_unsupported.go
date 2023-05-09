//go:build !linux
// +build !linux

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

type unsupportedRDTManager struct{}

// NewDefaultManager returns a defaultRDTManager.
func NewDefaultManager() RDTManager {
	return &unsupportedRDTManager{}
}

// CheckSupportRDT checks whether RDT is supported by the CPU and the kernel.
func (*unsupportedRDTManager) CheckSupportRDT() (bool, error) {
	return false, nil
}

// InitRDT performs some RDT-related initializations.
func (*unsupportedRDTManager) InitRDT() error {
	return nil
}

// ApplyTasks synchronizes the tasks of each CLOS.
func (*unsupportedRDTManager) ApplyTasks(clos string, tasks []string) error {
	return nil
}

// ApplyCAT applies the CAT configurations for each CLOS.
func (*unsupportedRDTManager) ApplyCAT(clos string, cat map[int]int) error {
	return nil
}

// ApplyMBA applies the MBA configurations for each CLOS.
func (*unsupportedRDTManager) ApplyMBA(clos string, mba map[int]int) error {
	return nil
}
