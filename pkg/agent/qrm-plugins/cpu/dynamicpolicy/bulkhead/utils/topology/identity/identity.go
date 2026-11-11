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

package identity

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
)

var (
	ErrCgroupIdentityUnsupported = errors.New("stable cgroup identity is unsupported")
	ErrCgroupIdentityChanged     = errors.New("cgroup identity changed during read")
)

// CgroupIdentity identifies a cgroup directory independently of its path.
// Linux device and inode are intentionally used instead of mtime: directory
// mtimes can change when descendants are created or removed without replacing
// the cgroup itself.
type CgroupIdentity struct {
	Device uint64
	Inode  uint64
}

// ChildRef identifies an immediate child by name and stable identity.
type ChildRef struct {
	Name     string
	Identity CgroupIdentity
}

// ReadStableEntry brackets read with identity checks so callers
// never accept data read across deletion and recreation of the same path.
func ReadStableEntry(path string, read func() error) (CgroupIdentity, error) {
	before, err := StatCgroupIdentity(path)
	if err != nil {
		return CgroupIdentity{}, err
	}
	readErr := read()
	after, identityErr := StatCgroupIdentity(path)
	if identityErr != nil {
		return CgroupIdentity{}, identityErr
	}
	if before != after {
		return CgroupIdentity{}, fmt.Errorf("%w: path=%q before=%v after=%v", ErrCgroupIdentityChanged, path, before, after)
	}
	if readErr != nil {
		return CgroupIdentity{}, readErr
	}
	return before, nil
}

// ChildrenFingerprint returns an order-independent fingerprint of child
// names and identities. A child recreated at the same path changes the result.
func ChildrenFingerprint(children []ChildRef) string {
	refs := append([]ChildRef(nil), children...)
	sort.Slice(refs, func(i, j int) bool {
		return refs[i].Name < refs[j].Name
	})

	hash := sha256.New()
	var encoded [8]byte
	for _, ref := range refs {
		binary.LittleEndian.PutUint64(encoded[:], uint64(len(ref.Name)))
		_, _ = hash.Write(encoded[:])
		_, _ = hash.Write([]byte(ref.Name))
		binary.LittleEndian.PutUint64(encoded[:], ref.Identity.Device)
		_, _ = hash.Write(encoded[:])
		binary.LittleEndian.PutUint64(encoded[:], ref.Identity.Inode)
		_, _ = hash.Write(encoded[:])
	}
	return hex.EncodeToString(hash.Sum(nil))
}
