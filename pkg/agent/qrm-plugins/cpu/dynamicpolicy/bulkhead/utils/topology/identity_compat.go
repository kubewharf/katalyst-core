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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/identity"
)

// This file is a compatibility shim: the cgroup-identity platform abstraction
// has been extracted into topology/identity, but the rest of the topology
// package (and external plugins) historically referenced these symbols
// unqualified. Type aliases and var re-exports keep that surface intact while
// the implementation lives in the subpackage. New code should import
// topology/identity directly.

// CgroupIdentity identifies a cgroup directory independently of its path.
type CgroupIdentity = identity.CgroupIdentity

// ChildRef identifies an immediate child by name and stable identity.
type ChildRef = identity.ChildRef

var (
	// ErrCgroupIdentityUnsupported reports that stable cgroup identity is
	// unavailable on the current platform.
	ErrCgroupIdentityUnsupported = identity.ErrCgroupIdentityUnsupported
	// ErrCgroupIdentityChanged indicates the cgroup identity changed mid-read.
	ErrCgroupIdentityChanged = identity.ErrCgroupIdentityChanged
)

// StatCgroupIdentity returns the platform device/inode identity for path.
var StatCgroupIdentity = identity.StatCgroupIdentity

// ReadStableEntry brackets a read with identity checks.
var ReadStableEntry = identity.ReadStableEntry

// ChildrenFingerprint returns an order-independent fingerprint of child refs.
var ChildrenFingerprint = identity.ChildrenFingerprint
