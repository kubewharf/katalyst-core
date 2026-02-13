//go:build linux
// +build linux

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

package hostwatermark

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetIntIfDifferent(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "sysctl")
	require.NoError(t, os.WriteFile(f, []byte("10\n"), 0o644))

	oldVal, newVal, changed, err := setIntIfDifferent(f, 10)
	require.NoError(t, err)
	require.Equal(t, int64(10), oldVal)
	require.Equal(t, int64(10), newVal)
	require.False(t, changed)

	oldVal, newVal, changed, err = setIntIfDifferent(f, 20)
	require.NoError(t, err)
	require.Equal(t, int64(10), oldVal)
	require.Equal(t, int64(20), newVal)
	require.True(t, changed)

	val, err := getIntFromFile(f)
	require.NoError(t, err)
	require.Equal(t, int64(20), val)
}

func TestGetIntFromFileInvalid(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	f := filepath.Join(tmp, "bad")
	require.NoError(t, os.WriteFile(f, []byte("abc\n"), 0o644))

	_, err := getIntFromFile(f)
	require.Error(t, err)
}
