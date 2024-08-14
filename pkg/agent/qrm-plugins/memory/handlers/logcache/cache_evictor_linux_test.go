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
)

func TestEvictFileCache(t *testing.T) {
	t.Parallel()

	testcases := []struct {
		filePath      string
		expectedError bool
	}{
		{
			filePath:      "",
			expectedError: true,
		},
		{
			filePath:      "writeonly.log",
			expectedError: true,
		},
		{
			filePath:      "regular.log",
			expectedError: false,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.filePath, func(t *testing.T) {
			file, err := os.Lstat(tc.filePath)
			if err == nil && file.Mode().IsRegular() {
				err = EvictFileCache(tc.filePath, file.Size())
			}

			if (err != nil) != tc.expectedError {
				t.Errorf("evict file cache error %v, got %v, error: %v", err != nil, tc.expectedError, err)
			}
		})
	}
}
