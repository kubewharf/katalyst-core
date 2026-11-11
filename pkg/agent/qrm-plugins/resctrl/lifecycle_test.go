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
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLifecycleConcurrentEnsureReusesDirectoryIdentity(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	service := NewCLOSLifecycleService(root)

	var wg sync.WaitGroup
	results := make(chan ResolvedCLOS, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resolved, err := service.EnsurePendingCLOS(context.Background(), "shared-50")
			require.NoError(t, err)
			results <- resolved
		}()
	}
	wg.Wait()
	close(results)
	first := <-results
	second := <-results
	require.Equal(t, first.Generation, second.Generation)
	require.Equal(t, first.Identity, second.Identity)
	require.Equal(t, ActivationActive, first.Phase)
	require.Equal(t, "shared-50", first.ID)
	require.DirExists(t, filepath.Join(root, "shared-50"))
}

func TestLifecycleKeepsDefaultAndExplicitSubgroupAsIndependentPhysicalCLOS(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	service := NewCLOSLifecycleService(root)

	defaultShare, err := service.EnsurePendingCLOS(context.Background(), "shared-50")
	require.NoError(t, err)
	explicitShare, err := service.EnsurePendingCLOS(context.Background(), "share-50")
	require.NoError(t, err)

	require.Equal(t, "shared-50", defaultShare.ID)
	require.Equal(t, "share-50", explicitShare.ID)
	require.NotEqual(t, defaultShare.Identity, explicitShare.Identity)
	require.DirExists(t, filepath.Join(root, "shared-50"))
	require.DirExists(t, filepath.Join(root, "share-50"))
}

func TestLifecycleRejectsInvalidCLOSIDs(t *testing.T) {
	t.Parallel()

	for _, closID := range []string{"", ".", "..", filepath.Join(string(filepath.Separator), "absolute"), "nested/clos", `nested\clos`} {
		closID := closID
		t.Run(closID, func(t *testing.T) {
			t.Parallel()

			service := NewCLOSLifecycleService(t.TempDir())
			_, err := service.EnsurePendingCLOS(context.Background(), closID)
			require.ErrorContains(t, err, "invalid clos id")
			require.ErrorContains(t, service.DeleteCLOS(context.Background(), closID), "invalid clos id")
		})
	}
}

func TestLifecycleDeleteRemovesEmptyAndRejectsNonEmpty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	service := NewCLOSLifecycleService(root)

	empty, err := service.EnsurePendingCLOS(context.Background(), "shared-50")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "shared-50", "tasks"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "shared-50", "cpus"), nil, 0o600))
	require.NoError(t, service.DeleteCLOS(context.Background(), empty.ID))
	require.NoDirExists(t, filepath.Join(root, "shared-50"))

	owned, err := service.EnsurePendingCLOS(context.Background(), "clos-a")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "clos-a", "cpus"), nil, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "clos-a", "tasks"), []byte("1"), 0o600))
	require.Error(t, service.DeleteCLOS(context.Background(), owned.ID))
	require.DirExists(t, filepath.Join(root, "clos-a"))
}
