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

package general

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestFileUtils(t *testing.T) {
	t.Parallel()

	// test to read from none-existed and existed files
	filename := "/tmp/TestFileUtils"
	_, err := ReadFileIntoLines(filename)
	assert.NotNil(t, err)

	data := []byte("test-1\ntest-2")
	err = ioutil.WriteFile(filename, data, 0o777)
	assert.NoError(t, err)
	defer func() {
		_ = os.Remove(filename)
	}()

	contents, err := ReadFileIntoLines(filename)
	assert.NoError(t, err)
	assert.Equal(t, 2, len(contents))
}

func Test_fileUniqueLock(t *testing.T) {
	t.Parallel()

	lockPath := "/tmp/Test_fileUniqueLock"

	flock, err := GetUniqueLock(lockPath)
	if err != nil {
		t.Errorf("GetUniqueLock() error = %v, wantErr %v", err, nil)
		return
	}

	_, err = getUniqueLockWithTimeout(lockPath, time.Millisecond*100, 3)
	if err == nil {
		t.Errorf("GetNode() error = %v, wantErr not nil", err)
		return
	}

	ReleaseUniqueLock(flock)
	flock, err = GetUniqueLock(lockPath)
	if err != nil {
		t.Errorf("GetUniqueLock() error = %v, wantErr %v", err, nil)
		return
	}
	ReleaseUniqueLock(flock)
}

func TestReadLines(t *testing.T) {
	t.Parallel()

	// Create a temporary file for testing
	tempDir := t.TempDir()
	fileName := filepath.Join(tempDir, "test.txt")

	type args struct {
		filePath string
		content  string
	}
	tests := []struct {
		name    string
		args    args
		want    []string
		wantErr bool
	}{{
		name: "empty file",
		args: args{
			filePath: fileName + "-1",
			content:  "",
		},
		want:    []string{},
		wantErr: true,
	}, {
		name: "single line",
		args: args{
			filePath: fileName + "-2",
			content:  "hello world",
		},
		want:    []string{"hello world"},
		wantErr: false,
	}, {
		name: "multiple lines",
		args: args{
			filePath: fileName + "-3",
			content:  "line1\nline2\nline3",
		},
		want:    []string{"line1", "line2", "line3"},
		wantErr: false,
	}, {
		name: "non-existent file",
		args: args{
			filePath: filepath.Join(tempDir, "non-existent.txt"),
			content:  "",
		},
		want:    nil,
		wantErr: true,
	}}

	for _, tt := range tests {
		tt := tt
		wantErr := tt.wantErr

		writeFile := func() error {
			if !wantErr && tt.args.content != "" {
				return os.WriteFile(tt.args.filePath, []byte(tt.args.content), 0o644)
			}
			return nil
		}

		if err := writeFile(); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadLines(tt.args.filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadLines() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !equalStringSlices(got, tt.want) {
				t.Errorf("ReadLines() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadInt64FromFile(t *testing.T) {
	t.Parallel()

	// Create a temporary file for testing
	tempDir := t.TempDir()
	fileName := filepath.Join(tempDir, "test.txt")

	type args struct {
		filePath string
		content  string
	}
	tests := []struct {
		name    string
		args    args
		want    int64
		wantErr bool
	}{{
		name: "empty file",
		args: args{
			filePath: fileName + "-1",
			content:  "",
		},
		want:    -1,
		wantErr: true,
	}, {
		name: "valid number",
		args: args{
			filePath: fileName + "-2",
			content:  "123",
		},
		want:    123,
		wantErr: false,
	}, {
		name: "negative number",
		args: args{
			filePath: fileName + "-3",
			content:  "-456",
		},
		want:    -456,
		wantErr: false,
	}, {
		name: "invalid number",
		args: args{
			filePath: fileName + "-4",
			content:  "abc",
		},
		want:    -1,
		wantErr: true,
	}, {
		name: "non-existent file",
		args: args{
			filePath: filepath.Join(tempDir, "non-existent.txt"),
			content:  "",
		},
		want:    -1,
		wantErr: true,
	}}

	for _, tt := range tests {
		tt := tt

		writeFile := func() error {
			if tt.args.content != "" {
				return os.WriteFile(tt.args.filePath, []byte(tt.args.content), 0o644)
			}
			return nil
		}

		if err := writeFile(); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadInt64FromFile(tt.args.filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadInt64FromFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ReadInt64FromFile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestReadUint64FromFile(t *testing.T) {
	t.Parallel()

	// Create a temporary file for testing
	tempDir := t.TempDir()
	fileName := filepath.Join(tempDir, "test.txt")

	type args struct {
		filePath string
		content  string
	}
	tests := []struct {
		name    string
		args    args
		want    uint64
		wantErr bool
	}{{
		name: "valid number",
		args: args{
			filePath: fileName + "-1",
			content:  "123",
		},
		want:    123,
		wantErr: false,
	}, {
		name: "large number",
		args: args{
			filePath: fileName + "-2",
			content:  "18446744073709551615",
		},
		want:    18446744073709551615,
		wantErr: false,
	}, {
		name: "invalid number",
		args: args{
			filePath: fileName + "-3",
			content:  "abc",
		},
		want:    0,
		wantErr: true,
	}, {
		name: "negative number",
		args: args{
			filePath: fileName + "-4",
			content:  "-456",
		},
		want:    0,
		wantErr: true,
	}, {
		name: "empty file",
		args: args{
			filePath: fileName + "-5",
			content:  "",
		},
		want:    0,
		wantErr: true,
	}, {
		name: "non-existent file",
		args: args{
			filePath: filepath.Join(tempDir, "non-existent.txt"),
			content:  "",
		},
		want:    0,
		wantErr: true,
	}}

	for _, tt := range tests {
		tt := tt

		writeFile := func() error {
			if tt.args.content != "" {
				return os.WriteFile(tt.args.filePath, []byte(tt.args.content), 0o644)
			}
			return nil
		}

		if err := writeFile(); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ReadUint64FromFile(tt.args.filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("ReadUint64FromFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("ReadUint64FromFile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetFileInode(t *testing.T) {
	t.Parallel()

	// Create a temporary file for testing
	tempDir := t.TempDir()
	fileName := filepath.Join(tempDir, "test.txt")
	if err := os.WriteFile(fileName, []byte("content"), 0o644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	type args struct {
		file string
	}
	tests := []struct {
		name    string
		args    args
		want    uint64
		wantErr bool
	}{{
		name: "valid file",
		args: args{
			file: fileName,
		},
		want:    0, // We can't know the exact inode, but we expect it to be non-zero
		wantErr: false,
	}, {
		name: "non-existent file",
		args: args{
			file: filepath.Join(tempDir, "non-existent.txt"),
		},
		want:    0,
		wantErr: true,
	}}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetFileInode(tt.args.file)
			if (err != nil) != tt.wantErr {
				t.Errorf("GetFileInode() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.name == "valid file" {
				if got == 0 {
					t.Error("GetFileInode() returned 0 for valid file")
				}
			} else if got != tt.want {
				t.Errorf("GetFileInode() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseLinuxListFormatFromFile(t *testing.T) {
	t.Parallel()
	// Create a temporary file for testing
	tempDir := t.TempDir()
	fileName := filepath.Join(tempDir, "test.txt")

	type args struct {
		filePath string
		content  string
	}
	tests := []struct {
		name    string
		args    args
		want    []int64
		wantErr bool
	}{
		{
			name: "empty file",
			args: args{
				filePath: fileName + "-1",
				content:  "",
			},
			want:    nil,
			wantErr: true,
		},
		{
			name: "valid content",
			args: args{
				filePath: fileName + "-2",
				content:  "1-3,5,7-9",
			},
			want:    []int64{1, 2, 3, 5, 7, 8, 9},
			wantErr: false,
		},
		{
			name: "non-existent file",
			args: args{
				filePath: filepath.Join(tempDir, "non-existent.txt"),
				content:  "",
			},
			want:    nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt

		writeFile := func() error {
			if tt.args.content != "" {
				return os.WriteFile(tt.args.filePath, []byte(tt.args.content), 0o644)
			}
			return nil
		}

		if err := writeFile(); err != nil {
			t.Fatalf("Failed to write test file: %v", err)
		}

		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := ParseLinuxListFormatFromFile(tt.args.filePath)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseLinuxListFormatFromFile() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !equalInt64Slices(got, tt.want) {
				t.Errorf("ParseLinuxListFormatFromFile() = %v, want %v", got, tt.want)
			}
		})
	}
}
