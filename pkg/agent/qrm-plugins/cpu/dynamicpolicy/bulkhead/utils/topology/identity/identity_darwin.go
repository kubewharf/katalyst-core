//go:build darwin
// +build darwin

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
	"fmt"
	"os"
	"syscall"
)

// StatCgroupIdentity returns the directory device and inode identity for path.
func StatCgroupIdentity(path string) (CgroupIdentity, error) {
	info, err := os.Stat(path)
	if err != nil {
		return CgroupIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return CgroupIdentity{}, fmt.Errorf("read cgroup identity %q: unexpected stat payload %T", path, info.Sys())
	}
	return CgroupIdentity{
		Device: uint64(stat.Dev),
		Inode:  stat.Ino,
	}, nil
}
