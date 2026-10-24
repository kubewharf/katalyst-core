/*
Copyright 2026 The Katalyst Authors.

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

package resctrl

import (
	"fmt"
	"os"
	"syscall"
)

type ActivationPhase string

const (
	ActivationActive ActivationPhase = "active"
)

type ResolvedCLOS struct {
	ID         string
	Identity   DirectoryIdentity
	Generation uint64
	Phase      ActivationPhase
}

// DirectoryIdentity identifies one concrete incarnation of a CLOS directory.
type DirectoryIdentity struct {
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
}

func DirectoryIdentityForPath(path string) (DirectoryIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return DirectoryIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return DirectoryIdentity{}, fmt.Errorf("unsupported filesystem identity for %q", path)
	}
	return DirectoryIdentity{Device: uint64(stat.Dev), Inode: stat.Ino}, nil //nolint:unconvert // Stat_t.Dev is int32 on darwin
}

func SameDirectoryIdentity(path string, expected *DirectoryIdentity) (bool, error) {
	if expected == nil {
		return false, nil
	}
	actual, err := DirectoryIdentityForPath(path)
	if err != nil {
		return false, err
	}
	return actual == *expected, nil
}
