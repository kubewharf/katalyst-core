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

package topology

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/model"
)

// This file is a compatibility shim: the leaf value types shared between the
// parent topology package and the deadlock/phase subpackages have been
// extracted into topology/model so that the subpackages can depend on model
// instead of on the parent package (which would otherwise be an import cycle).
// Type aliases keep the historical unqualified names available inside this
// package and for any external consumer. New code should import topology/model
// directly.

// DomainID identifies an ownership domain without relying on cgroup names.
type DomainID = model.DomainID

// SnapshotID fingerprints the fully-observed hierarchy generation.
type SnapshotID = model.SnapshotID

const (
	DomainPrimary DomainID = model.DomainPrimary
	DomainReclaim DomainID = model.DomainReclaim
)
