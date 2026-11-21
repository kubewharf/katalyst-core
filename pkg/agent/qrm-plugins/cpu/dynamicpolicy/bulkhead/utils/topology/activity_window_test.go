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

package topology

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// The second membership read is an ordinary fallible operation. Its exact
// failure must stop collection without publishing a plausible empty result.
func TestCgroupActivityRecheckPropagatesFailure(t *testing.T) {
	t.Parallel()

	root := resolvedPath(t, t.TempDir())
	holder := filepath.Join(root, "holder")
	require.NoError(t, os.Mkdir(holder, 0o755))
	for name, value := range map[string]string{
		"cpuset.cpus": "0-3", "cpuset.mems": "0", "tasks": "", "cgroup.procs": "",
	} {
		require.NoError(t, os.WriteFile(filepath.Join(holder, name), []byte(value), 0o644))
	}
	driver := newTestCgroupV1Driver(t, root, nil)
	identity, err := driver.StatIdentity(context.Background(), "holder")
	require.NoError(t, err)
	sentinel := errors.New("membership recheck denied")
	read := driver.readFileAt
	taskReads := 0
	driver.readFileAt = func(fd int, name string) ([]byte, error) {
		if name == "tasks" {
			taskReads++
			if taskReads == 2 {
				return []byte(""), sentinel
			}
		}
		return read(fd, name)
	}
	entry, err := driver.ReadEntryWithActivity(context.Background(), "holder", identity)
	require.ErrorIs(t, err, sentinel)
	require.Equal(t, 2, taskReads)
	require.Equal(t, EntryState{}, entry)
}

// A pinned directory proves identity, not stable membership. Attach a task or
// process after its first empty read without changing children or cpuset.
func TestCgroupActivityRejectsMembershipChangeDuringRead(t *testing.T) {
	t.Parallel()

	for _, change := range []struct {
		membership string
		before     string
		after      string
	}{
		{"tasks", "", "123\n"},
		{"tasks", "123\n", ""},
		{"cgroup.procs", "", "123\n"},
		{"cgroup.procs", "123\n", ""},
	} {
		name := change.membership + "/attach"
		if change.after == "" {
			name = change.membership + "/detach"
		}
		change := change
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := resolvedPath(t, t.TempDir())
			holder := filepath.Join(root, "holder")
			require.NoError(t, os.Mkdir(holder, 0o755))
			for name, value := range map[string]string{
				"cpuset.cpus": "0-3", "cpuset.mems": "0", "tasks": "", "cgroup.procs": "",
			} {
				if name == change.membership {
					value = change.before
				}
				require.NoError(t, os.WriteFile(filepath.Join(holder, name), []byte(value), 0o644))
			}
			driver := newTestCgroupV1Driver(t, root, nil)
			identity, err := driver.StatIdentity(context.Background(), "holder")
			require.NoError(t, err)
			read := driver.readFileAt
			attached := false
			driver.readFileAt = func(fd int, name string) ([]byte, error) {
				raw, err := read(fd, name)
				if name == "cpuset.cpus" && !attached {
					attached = true
					require.NoError(t, os.WriteFile(filepath.Join(holder, change.membership), []byte(change.after), 0o644))
				}
				return raw, err
			}
			entry, err := driver.ReadEntryWithActivity(context.Background(), "holder", identity)
			require.True(t, attached)
			require.ErrorIs(t, err, ErrCgroupIdentityChanged, "changed membership cannot support a stale empty observation")
			require.Equal(t, EntryState{}, entry, "no dirty observation may escape")
		})
	}
}
