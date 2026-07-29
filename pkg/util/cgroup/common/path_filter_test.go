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

package common

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGetExistingRelativeCgroupPaths(t *testing.T) {
	checkedPaths := make([]string, 0)
	originalPathExists := relativeCgroupPathExists
	relativeCgroupPathExists = func(path string) bool {
		checkedPaths = append(checkedPaths, path)
		return path == GetAbsCgroupPath(DefaultSelectedSubsys, "/existing")
	}
	defer func() {
		relativeCgroupPathExists = originalPathExists
	}()

	got := GetExistingRelativeCgroupPaths("/existing", "", "/missing")

	require.Equal(t, []string{"/existing"}, got)
	require.Equal(t, []string{
		GetAbsCgroupPath(DefaultSelectedSubsys, "/existing"),
		GetAbsCgroupPath(DefaultSelectedSubsys, "/missing"),
	}, checkedPaths)
	require.Nil(t, GetExistingRelativeCgroupPaths())
}

func TestGetExistingRelativeCgroupPathsForSubsys(t *testing.T) {
	checkedPaths := make([]string, 0)
	originalPathExists := relativeCgroupPathExists
	relativeCgroupPathExists = func(path string) bool {
		checkedPaths = append(checkedPaths, path)
		return path == GetAbsCgroupPath(CgroupSubsysMemory, "/existing")
	}
	defer func() {
		relativeCgroupPathExists = originalPathExists
	}()

	got := GetExistingRelativeCgroupPathsForSubsys(
		CgroupSubsysMemory,
		"/missing",
		"/existing",
	)

	require.Equal(t, []string{"/existing"}, got)
	require.Equal(t, []string{
		GetAbsCgroupPath(CgroupSubsysMemory, "/missing"),
		GetAbsCgroupPath(CgroupSubsysMemory, "/existing"),
	}, checkedPaths)
}
