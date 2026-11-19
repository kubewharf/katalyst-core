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

import "errors"

// ErrCATUnsupported indicates that this host cannot safely use the L3 CAT
// interface exposed by this manager.
var ErrCATUnsupported = errors.New("RDT CAT is unsupported")

// RDTManager provides methods that control RDT related resources.
// Note: OCI Spec and runC already support the configuration of RDT-related parameters, but CRI and containerd do not yet support it.
// Therefore, we plan to support the configuration of RDT-related parameters through NRI or CRI in the future.
type RDTManager interface {
	CheckSupportRDT() (bool, error)
	InitRDT() error
	ApplyTasks(clos string, tasks []string) error
	ApplyCAT(clos string, cat map[int]uint64) error
	ApplyMBA(clos string, mba map[int]int) error
	RunClosResourceUpdate(clos string, update func() (bool, error)) error
	InvalidateClos(clos string)
}
