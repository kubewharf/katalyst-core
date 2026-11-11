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

package compatibility

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type testVersionCheckpoint struct {
	VersionCheckpoint
	fileName string
}

func (c *testVersionCheckpoint) FileName() string {
	return c.fileName
}

func TestVersionStorePersistsIndependentFeatureCheckpoints(t *testing.T) {
	t.Parallel()

	store := NewVersionStore(t.TempDir())
	eviction := &testVersionCheckpoint{
		VersionCheckpoint: NewVersionCheckpoint("policy/v1"),
		fileName:          "eviction-version",
	}
	runtime := &testVersionCheckpoint{
		VersionCheckpoint: NewVersionCheckpoint("v1alpha2"),
		fileName:          "runtime-version",
	}
	require.NoError(t, store.Put(eviction))
	require.NoError(t, store.Put(runtime))

	cachedEviction := &testVersionCheckpoint{fileName: eviction.FileName()}
	found, err := store.Get(cachedEviction)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "policy/v1", cachedEviction.Version())

	cachedRuntime := &testVersionCheckpoint{fileName: runtime.FileName()}
	found, err = store.Get(cachedRuntime)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "v1alpha2", cachedRuntime.Version())
}

func TestVersionStoreTreatsMissingCheckpointAsCacheMiss(t *testing.T) {
	t.Parallel()

	store := NewVersionStore(t.TempDir())
	checkpoint := &testVersionCheckpoint{fileName: "missing"}
	found, err := store.Get(checkpoint)
	require.NoError(t, err)
	require.False(t, found)
}

func TestVersionStoreReturnsNilWhenPersistenceIsDisabled(t *testing.T) {
	t.Parallel()

	require.Nil(t, NewVersionStore(""))
}
