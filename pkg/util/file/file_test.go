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

package file

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFilesEqual(t *testing.T) {
	t.Parallel()
	tmpDir := t.TempDir()

	createTempFile := func(content string) string {
		f, err := os.CreateTemp(tmpDir, "testfile-")
		assert.NoError(t, err)
		_, err = f.WriteString(content)
		assert.NoError(t, err)
		err = f.Close()
		assert.NoError(t, err)
		return f.Name()
	}

	jsonContent := `{"key":"value1", "number": 1}`
	identicalJsonContent := `{"key":"value1", "number": 1}`
	differentJsonContent := `{"key":"value1", "number": 0}`

	tests := []struct {
		name      string
		setup     func() (path1, path2 string)
		wantEqual bool
		wantErr   bool
	}{
		{
			name: "identical text files",
			setup: func() (string, string) {
				path1 := createTempFile("hello world")
				path2 := createTempFile("hello world")
				return path1, path2
			},
			wantEqual: true,
			wantErr:   false,
		},
		{
			name: "different text files",
			setup: func() (string, string) {
				path1 := createTempFile("hello world")
				path2 := createTempFile("hello there")
				return path1, path2
			},
			wantEqual: false,
			wantErr:   false,
		},
		{
			name: "different size",
			setup: func() (string, string) {
				path1 := createTempFile("hello world")
				path2 := createTempFile("hello")
				return path1, path2
			},
			wantEqual: false,
			wantErr:   false,
		},
		{
			name: "one file does not exist",
			setup: func() (string, string) {
				path1 := createTempFile("hello world")
				return path1, "non-existent-file"
			},
			wantEqual: false,
			wantErr:   true,
		},
		{
			name: "empty files",
			setup: func() (string, string) {
				path1 := createTempFile("")
				path2 := createTempFile("")
				return path1, path2
			},
			wantEqual: true,
			wantErr:   false,
		},
		{
			name: "identical json files",
			setup: func() (string, string) {
				path1 := createTempFile(jsonContent)
				path2 := createTempFile(identicalJsonContent)
				return path1, path2
			},
			wantEqual: true,
			wantErr:   false,
		},
		{
			name: "different json files",
			setup: func() (string, string) {
				path1 := createTempFile(jsonContent)
				path2 := createTempFile(differentJsonContent)
				return path1, path2
			},
			wantEqual: false,
			wantErr:   false,
		},
		{
			name: "copied files should be equal",
			setup: func() (string, string) {
				path1 := createTempFile(jsonContent)
				path2 := path1 + ".copy"

				input, err := os.ReadFile(path1)
				assert.NoError(t, err)

				err = os.WriteFile(path2, input, 0o644)
				assert.NoError(t, err)

				return path1, path2
			},
			wantEqual: true,
			wantErr:   false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path1, path2 := tt.setup()

			equal, err := FilesEqual(path1, path2)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.wantEqual, equal)
		})
	}
}
