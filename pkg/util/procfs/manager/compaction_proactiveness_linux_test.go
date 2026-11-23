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

package manager

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestVMCompactionProactivenessReadAndWrite(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "parameter")
	for _, value := range []int32{0, 20, 100} {
		require.NoError(t, os.WriteFile(path, []byte("20"), 0o600))
		require.NoError(t, WriteVMCompactionProactivenessAtPath(path, value))
		actual, err := ReadVMCompactionProactivenessAtPath(path)
		require.NoError(t, err)
		require.Equal(t, value, actual)
	}
	for _, text := range []string{"", "invalid", "1.5", "-1", "101", "4294967296", "9223372036854775808"} {
		require.NoError(t, os.WriteFile(path, []byte(text), 0o600))
		_, err := ReadVMCompactionProactivenessAtPath(path)
		require.Error(t, err, text)
	}
}

func TestVMCompactionProactivenessRejectsBeforeWriting(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "parameter")
	require.NoError(t, os.WriteFile(path, []byte("20"), 0o600))
	for _, value := range []int32{-1, 101} {
		require.Error(t, WriteVMCompactionProactivenessAtPath(path, value))
		actual, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, "20", string(actual))
	}
}

func TestVMCompactionProactivenessNeverCreatesOrTruncates(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "parameter")
	require.Error(t, WriteVMCompactionProactivenessAtPath(path, 20))
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err))
	_, err = ReadVMCompactionProactivenessAtPath(path)
	require.Error(t, err)
	require.NoError(t, os.WriteFile(path, []byte("20\n   \n"), 0o600))
	require.NoError(t, WriteVMCompactionProactivenessAtPath(path, 0))
	actual, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "0\n\n   \n", string(actual), "ordinary-file suffix must not be truncated")
	value, err := ReadVMCompactionProactivenessAtPath(path)
	require.NoError(t, err)
	require.Zero(t, value)
	require.Error(t, WriteVMCompactionProactivenessAtPath(filepath.Dir(path), 20))
}
