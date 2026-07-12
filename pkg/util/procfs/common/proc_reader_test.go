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

package common

import (
	"errors"
	iofs "io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	utilfs "github.com/kubewharf/katalyst-core/pkg/util/fs"
)

type procfsFakeFS struct {
	files   map[string][]byte
	dirs    map[string][]iofs.DirEntry
	readErr map[string]error
}

func newProcfsFakeFS() *procfsFakeFS {
	return &procfsFakeFS{
		files:   map[string][]byte{},
		dirs:    map[string][]iofs.DirEntry{},
		readErr: map[string]error{},
	}
}

func (f *procfsFakeFS) ReadFile(path string) ([]byte, error) {
	if err, ok := f.readErr[path]; ok {
		return nil, err
	}
	b, ok := f.files[path]
	if !ok {
		return nil, os.ErrNotExist
	}
	return append([]byte(nil), b...), nil
}

func (f *procfsFakeFS) WriteFile(path string, b []byte, _ os.FileMode) error {
	f.files[path] = append([]byte(nil), b...)
	return nil
}

func (f *procfsFakeFS) Exists(path string) bool {
	_, ok := f.files[path]
	return ok
}

func (f *procfsFakeFS) ReadDir(dir string) ([]iofs.DirEntry, error) {
	if err, ok := f.readErr[dir]; ok {
		return nil, err
	}
	entries, ok := f.dirs[dir]
	if !ok {
		return nil, os.ErrNotExist
	}
	return entries, nil
}

var _ utilfs.FS = (*procfsFakeFS)(nil)

type fakeDirEntry struct {
	name string
	dir  bool
}

func (e fakeDirEntry) Name() string                 { return e.name }
func (e fakeDirEntry) IsDir() bool                  { return e.dir }
func (e fakeDirEntry) Type() iofs.FileMode          { return 0 }
func (e fakeDirEntry) Info() (iofs.FileInfo, error) { return nil, errors.New("unsupported") }

func TestParsePPIDFromStat_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		stat    string
		want    int
		wantErr bool
	}{
		{
			name: "normal init line",
			stat: "1 (systemd) S 0 1 1 0 -1 4194560 12345",
			want: 0,
		},
		{
			name: "kthreadd child",
			stat: "42 (kworker/u8:1) I 2 0 0 0 -1 69238880 0",
			want: 2,
		},
		{
			name: "comm with spaces and parens",
			stat: "17 (weird ) name)) S 1 17 17 0 -1 4194304 0",
			want: 1,
		},
		{
			name:    "missing closing paren",
			stat:    "17 systemd S 1 17 17",
			wantErr: true,
		},
		{
			name:    "truncated after paren",
			stat:    "17 (systemd)",
			wantErr: true,
		},
		{
			name:    "too few fields",
			stat:    "17 (systemd) S",
			wantErr: true,
		},
		{
			name:    "ppid not numeric",
			stat:    "17 (systemd) S bogus 17 17",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parsePPIDFromStat([]byte(tt.stat))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parsePPIDFromStat(%q) = (%d,nil), want error", tt.stat, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePPIDFromStat(%q): %v", tt.stat, err)
			}
			if got != tt.want {
				t.Fatalf("parsePPIDFromStat(%q) = %d, want %d", tt.stat, got, tt.want)
			}
		})
	}
}

func TestOSProcReader_ListPIDsFiltersNonNumericDirs(t *testing.T) {
	t.Parallel()

	f := newProcfsFakeFS()
	f.dirs["/proc"] = []iofs.DirEntry{
		fakeDirEntry{name: "1", dir: true},
		fakeDirEntry{name: "42", dir: true},
		fakeDirEntry{name: "self", dir: true},
		fakeDirEntry{name: "sys", dir: true},
		fakeDirEntry{name: "0", dir: true},
		fakeDirEntry{name: "kmsg", dir: false},
	}

	pids, err := NewProcReader(f, "/proc").ListPIDs()
	if err != nil {
		t.Fatalf("ListPIDs: %v", err)
	}

	got := map[int]bool{}
	for _, pid := range pids {
		got[pid] = true
	}
	if !got[1] || !got[42] {
		t.Fatalf("ListPIDs must include real pids; got %v", pids)
	}
	if len(pids) != 2 {
		t.Fatalf("ListPIDs must skip non-pid entries; got %v", pids)
	}
}

func TestOSProcReader_ListPIDsPropagatesReadDirError(t *testing.T) {
	t.Parallel()

	f := newProcfsFakeFS()
	f.readErr["/proc"] = errors.New("permission denied")

	if _, err := NewProcReader(f, "/proc").ListPIDs(); err == nil {
		t.Fatalf("ListPIDs must propagate ReadDir error")
	}
}

func TestOSProcReader_ReadProc_ClassifiesKernelThread(t *testing.T) {
	t.Parallel()

	f := newProcfsFakeFS()
	f.files["/proc/42/comm"] = []byte("kworker/u8:1\n")
	f.files["/proc/42/stat"] = []byte("42 (kworker/u8:1) I 2 0 0 0 -1 69238880 0")

	info, err := NewProcReader(f, "/proc").ReadProc(42)
	if err != nil {
		t.Fatalf("ReadProc: %v", err)
	}
	if info.PID != 42 || info.PPID != 2 || !info.IsKThread {
		t.Fatalf("ReadProc = %+v, want PID=42 PPID=2 IsKThread=true", info)
	}
	if info.Comm != "kworker/u8:1" {
		t.Fatalf("Comm = %q, want %q", info.Comm, "kworker/u8:1")
	}
}

func TestOSProcReader_ReadProc_PreservesCommWhitespace(t *testing.T) {
	t.Parallel()

	f := newProcfsFakeFS()
	f.files["/proc/43/comm"] = []byte(" worker \n")
	f.files["/proc/43/stat"] = []byte("43 ( worker ) S 1 43 43 0 -1 4194560 0")

	info, err := NewProcReader(f, "/proc").ReadProc(43)
	if err != nil {
		t.Fatalf("ReadProc: %v", err)
	}
	if info.Comm != " worker " {
		t.Fatalf("Comm = %q, want %q", info.Comm, " worker ")
	}
}

func TestOSProcReader_ReadProc_ClassifiesKthreaddItself(t *testing.T) {
	t.Parallel()

	f := newProcfsFakeFS()
	f.files["/proc/2/comm"] = []byte("kthreadd\n")
	f.files["/proc/2/stat"] = []byte("2 (kthreadd) S 0 0 0 0 -1 4194560 0")

	info, err := NewProcReader(f, "/proc").ReadProc(2)
	if err != nil {
		t.Fatalf("ReadProc: %v", err)
	}
	if !info.IsKThread {
		t.Fatalf("pid 2 must be classified as kthread: %+v", info)
	}
}

func TestOSProcReader_ReadProc_ClassifiesUserspace(t *testing.T) {
	t.Parallel()

	f := newProcfsFakeFS()
	f.files["/proc/1000/comm"] = []byte("bash\n")
	f.files["/proc/1000/stat"] = []byte("1000 (bash) S 999 1000 1000 0 -1 4194304 0")

	info, err := NewProcReader(f, "/proc").ReadProc(1000)
	if err != nil {
		t.Fatalf("ReadProc: %v", err)
	}
	if info.IsKThread {
		t.Fatalf("userspace PID must not be classified as kthread: %+v", info)
	}
	if info.PPID != 999 {
		t.Fatalf("PPID = %d, want 999", info.PPID)
	}
}

func TestOSProcReader_ReadProc_MissingCommOrStat(t *testing.T) {
	t.Parallel()

	fMissingComm := newProcfsFakeFS()
	fMissingComm.files["/proc/7/stat"] = []byte("7 (foo) S 1 7 7 0 -1 4194304 0")
	if _, err := NewProcReader(fMissingComm, "/proc").ReadProc(7); err == nil {
		t.Fatalf("ReadProc must fail when comm is missing")
	}

	fMissingStat := newProcfsFakeFS()
	fMissingStat.files["/proc/7/comm"] = []byte("foo\n")
	if _, err := NewProcReader(fMissingStat, "/proc").ReadProc(7); err == nil {
		t.Fatalf("ReadProc must fail when stat is missing")
	}
}

func TestOSProcReader_ReadProc_MalformedStat(t *testing.T) {
	t.Parallel()

	f := newProcfsFakeFS()
	f.files["/proc/8/comm"] = []byte("x\n")
	f.files["/proc/8/stat"] = []byte("no-parens-and-not-enough-fields")

	_, err := NewProcReader(f, "/proc").ReadProc(8)
	if err == nil {
		t.Fatalf("ReadProc must fail on malformed stat")
	}
	if !strings.Contains(err.Error(), "parse stat") {
		t.Fatalf("ReadProc error must wrap parse-stat context, got: %v", err)
	}
}

func TestOSProcReader_SchedSetaffinityDefensiveGuards(t *testing.T) {
	t.Parallel()

	r := NewProcReader(newProcfsFakeFS(), "/proc")
	if err := r.SchedSetaffinity(0, []int{1}); err == nil {
		t.Fatalf("SchedSetaffinity must reject pid<=0")
	}
	if err := r.SchedSetaffinity(-5, []int{1}); err == nil {
		t.Fatalf("SchedSetaffinity must reject negative pid")
	}
	if err := r.SchedSetaffinity(1, nil); err == nil {
		t.Fatalf("SchedSetaffinity must reject empty cpuset")
	}
	if err := r.SchedSetaffinity(1, []int{}); err == nil {
		t.Fatalf("SchedSetaffinity must reject empty cpuset slice")
	}
}

func TestOSProcReader_ListPIDsWithTempDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "1"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "kmsg"), []byte{}, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	pids, err := NewProcReader(utilfs.NewOSFS(), dir).ListPIDs()
	if err != nil {
		t.Fatalf("ListPIDs: %v", err)
	}
	if len(pids) != 1 || pids[0] != 1 {
		t.Fatalf("ListPIDs = %v, want [1]", pids)
	}
}
