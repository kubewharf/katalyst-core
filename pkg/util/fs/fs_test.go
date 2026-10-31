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

package fs

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeFS struct {
	files    map[string][]byte
	readErr  map[string]error
	writeErr map[string]error
	writes   []fakeWrite
}

type fakeWrite struct {
	path string
	data string
	mode os.FileMode
}

func newFakeFS() *fakeFS {
	return &fakeFS{
		files:    map[string][]byte{},
		readErr:  map[string]error{},
		writeErr: map[string]error{},
	}
}

func (f *fakeFS) ReadFile(path string) ([]byte, error) {
	if err, ok := f.readErr[path]; ok {
		return nil, err
	}
	b, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}

func (f *fakeFS) WriteFile(path string, b []byte, mode os.FileMode) error {
	if err, ok := f.writeErr[path]; ok {
		return err
	}
	f.files[path] = append([]byte(nil), b...)
	f.writes = append(f.writes, fakeWrite{
		path: path,
		data: string(b),
		mode: mode,
	})
	return nil
}

func (f *fakeFS) Exists(path string) bool {
	_, ok := f.files[path]
	return ok
}

func (f *fakeFS) ReadDir(string) ([]iofs.DirEntry, error) {
	return nil, errors.New("unsupported")
}

var _ FS = (*fakeFS)(nil)

func TestWriteStringIfChanged_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		current    string
		readErr    error
		writeErr   error
		want       string
		wantChange bool
		wantErr    bool
		wantWrites int
	}{
		{
			name:       "unchanged after trimming trailing newlines",
			current:    "hello\n",
			want:       "hello",
			wantChange: false,
			wantWrites: 0,
		},
		{
			name:       "different content writes",
			current:    "hello\n",
			want:       "world",
			wantChange: true,
			wantWrites: 1,
		},
		{
			name:       "missing file writes",
			readErr:    os.ErrNotExist,
			want:       "created",
			wantChange: true,
			wantWrites: 1,
		},
		{
			name:       "read error returns context without write",
			readErr:    errors.New("permission denied"),
			want:       "created",
			wantChange: false,
			wantErr:    true,
			wantWrites: 0,
		},
		{
			name:       "write error returns context",
			readErr:    os.ErrNotExist,
			writeErr:   errors.New("denied"),
			want:       "created",
			wantChange: false,
			wantErr:    true,
			wantWrites: 0,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			const path = "/tmp/value"
			fsys := newFakeFS()
			if tt.current != "" {
				fsys.files[path] = []byte(tt.current)
			}
			if tt.readErr != nil {
				fsys.readErr[path] = tt.readErr
			}
			if tt.writeErr != nil {
				fsys.writeErr[path] = tt.writeErr
			}

			changed, err := WriteStringIfChanged(fsys, path, tt.want, 0o644)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("WriteStringIfChanged() error = nil, want non-nil")
				}
				if !strings.Contains(err.Error(), path) {
					t.Fatalf("error %q must include path %q", err.Error(), path)
				}
			} else if err != nil {
				t.Fatalf("WriteStringIfChanged() error = %v", err)
			}
			if changed != tt.wantChange {
				t.Fatalf("changed = %v, want %v", changed, tt.wantChange)
			}
			if len(fsys.writes) != tt.wantWrites {
				t.Fatalf("writes = %d, want %d", len(fsys.writes), tt.wantWrites)
			}
			if tt.wantWrites > 0 {
				got := fsys.writes[0]
				if got.data != tt.want || got.mode != 0o644 {
					t.Fatalf("write = %+v, want data=%q mode=%#o", got, tt.want, 0o644)
				}
			}
		})
	}
}

func TestOSFS_RoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	fsys := NewOSFS()
	path := filepath.Join(dir, "hello.txt")

	if fsys.Exists(path) {
		t.Fatalf("Exists(%q) = true, want false", path)
	}
	if err := fsys.WriteFile(path, []byte("hi"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !fsys.Exists(path) {
		t.Fatalf("Exists(%q) = false, want true", path)
	}
	b, err := fsys.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(b) != "hi" {
		t.Fatalf("ReadFile = %q, want %q", string(b), "hi")
	}
	entries, err := fsys.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "hello.txt" {
		t.Fatalf("ReadDir = %v, want [hello.txt]", entries)
	}
}
