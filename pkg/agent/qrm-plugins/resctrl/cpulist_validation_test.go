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
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCPUListManagerRejectsMalformedTargetsWithoutReset(t *testing.T) {
	t.Parallel()
	for _, target := range []string{"2-1", "0-1-2", "1,3-2", "1,0-1-2"} {
		target := target
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, cpus), []byte("ffffffff"), 0o644))
			require.NoError(t, os.Mkdir(filepath.Join(root, "dedicated"), 0o755))
			path := filepath.Join(root, "dedicated", cpus)
			require.NoError(t, os.WriteFile(path, []byte("00000006"), 0o644))
			manager := newCPUListManagerWithUpdater(root, nil)
			require.Error(t, manager.ApplyCPUList(context.Background(), "dedicated", target))
			content, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, "00000006", string(content))
			require.Error(t, manager.ApplyCPUList(context.Background(), "missing", target))
			require.NoDirExists(t, filepath.Join(root, "missing"))
		})
	}
}

func TestCPUListManagerEpochAdvancesAfterAbsentPoll(t *testing.T) {
	t.Parallel()
	for _, removeRoot := range []bool{false, true} {
		removeRoot := removeRoot
		t.Run(fmt.Sprint(removeRoot), func(t *testing.T) {
			t.Parallel()
			root := filepath.Join(t.TempDir(), "resctrl")
			require.NoError(t, os.MkdirAll(filepath.Join(root, "dedicated"), 0o755))
			manager := newCPUListManagerWithUpdater(root, nil)
			first, err := manager.ListManagedClos(context.Background())
			require.NoError(t, err)
			require.Len(t, first, 1)
			path := filepath.Join(root, "dedicated")
			if removeRoot {
				path = root
			}
			require.NoError(t, os.RemoveAll(path))
			absent, err := manager.ListManagedClos(context.Background())
			require.NoError(t, err)
			require.Empty(t, absent)
			require.NoError(t, os.MkdirAll(filepath.Join(root, "dedicated"), 0o755))
			second, err := manager.ListManagedClos(context.Background())
			require.NoError(t, err)
			require.Len(t, second, 1)
			require.Greater(t, second[0].Epoch, first[0].Epoch)
			stable, err := manager.ListManagedClos(context.Background())
			require.NoError(t, err)
			require.Equal(t, second, stable)
			manager.mu.Lock()
			manager.closeAllHandlesLocked()
			manager.mu.Unlock()
		})
	}
}
