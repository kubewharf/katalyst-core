//go:build linux
// +build linux

// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rdt

import (
	"errors"
)

type defaultRDTManager struct{}

// NewDefaultManager returns a defaultRDTManager.
func NewDefaultManager() RDTManager {
	return &defaultRDTManager{}
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

// ApplyCAT applies the CAT configurations for each CLOS.
func (*defaultRDTManager) ApplyCAT(clos string, cat map[int]int) error {
	// TODO: implement ApplyCAT
	return errors.New("not implemented yet")
}

// ApplyMBA applies the MBA configurations for each CLOS.
func (*defaultRDTManager) ApplyMBA(clos string, mba map[int]int) error {
	// TODO: implement ApplyMBA
	return errors.New("not implemented yet")
}
