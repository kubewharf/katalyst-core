package logcache

import (
	"os"
	"testing"
)

func TestEvictFileCache(t *testing.T) {
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
