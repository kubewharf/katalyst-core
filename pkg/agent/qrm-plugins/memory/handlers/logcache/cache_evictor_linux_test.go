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

package logcache

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEvictFileCache(t *testing.T) {
	t.Parallel()

	testcases := []struct {
		name          string
		filePath      string
		expectedError bool
	}{
		{
			name:          "empty file path",
			filePath:      "",
			expectedError: true,
		},
		{
			name:          "failed due to permission",
			filePath:      "writeonly.log",
			expectedError: true,
		},
		{
			name:          "file not exist",
			filePath:      "non-exit.log",
			expectedError: true,
		},
	}

	for _, testcase := range testcases {
		tc := testcase
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file, err := os.Lstat(tc.filePath)
			if err == nil && file.Mode().IsRegular() {
				err = EvictFileCache(tc.filePath, file.Size())
			}

			if (err != nil) != tc.expectedError {
				t.Errorf("evict file cache error %v, got %v, error: %v", err != nil, tc.expectedError, err)
			}
		})
	}

	regularFile, err := os.Create("regular.log")
	require.NoError(t, err)

	file, err := os.Lstat(regularFile.Name())
	if err == nil && file.Mode().IsRegular() {
		err = EvictFileCache(regularFile.Name(), file.Size())
	}
	if err != nil {
		t.Errorf("evict file cache unexpected error: %v", err)
	}

	defer func() {
		_ = regularFile.Close()
		_ = os.Remove(regularFile.Name())
	}()
}
