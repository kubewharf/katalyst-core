//go:build amd64 && linux
// +build amd64,linux

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

package dynamicpolicy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gotest.tools/v3/assert"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	vmstatContentFormat = `
nr_free_pages %d
nr_zone_inactive_anon 166901
nr_zone_active_anon 51417730
nr_zone_inactive_file 55674772
`
)

func TestGetNumaNodeFreeMemMB(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		numaPages map[int]int64
		want      map[int]int
		wantErr   bool
	}{
		{
			name:      "empty numa",
			numaPages: nil,
			want:      make(map[int]int),
			wantErr:   false,
		},
		{
			name: "one numa",
			numaPages: map[int]int64{
				0: 10000,
			},
			want: map[int]int{
				0: 9,
			},
			wantErr: false,
		},
		{
			name: "two numa",
			numaPages: map[int]int64{
				0: 10000,
				2: 20000,
			},
			want: map[int]int{
				0: 3,
				2: 6,
			},
			wantErr: false,
		},
		{
			name: "three numa",
			numaPages: map[int]int64{
				0: 10000,
				2: 20000,
				6: 4000,
			},
			want: map[int]int{
				0: 2,
				2: 5,
				6: 1,
			},
			wantErr: false,
		},
		{
			name: "three small numa",
			numaPages: map[int]int64{
				0: 10,
				2: 10,
				6: 10,
			},
			want:    make(map[int]int),
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			var numas []int
			for numa, pages := range tt.numaPages {
				content := fmt.Sprintf(vmstatContentFormat, pages)
				generateNodeVMStatFile(t, tmpDir, numa, content)
				numas = append(numas, numa)
			}

			result, err := getNumasFreeMemRatio(tmpDir, numas)
			if (err != nil) != tt.wantErr {
				t.Errorf("test name: %s for getNumasFreeMemRatio error = %v, wantErr %v", tt.name, err, tt.wantErr)
				return
			}
			assert.DeepEqual(t, tt.want, result)
		})
	}
}

func TestGetNumaNodeFreeMemMB_VMStatFileNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "read numa file failed",
			wantErr: true,
			errMsg:  "failed to ReadFile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			_, err := getNumasFreeMemRatio(tmpDir, []int{0})
			if (err != nil) != tt.wantErr {
				t.Errorf("test name: %s for getNumasFreeMemRatio error = %v, wantErr %v", tt.name, err, tt.wantErr)
				return
			}

			assert.Assert(t, strings.Contains(err.Error(), tt.errMsg))
		})
	}
}

func TestGetNumaNodeFreeMemMB_GetFreePagesFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "empty file",
			content: "",
			wantErr: true,
			errMsg:  "failed to find nr_free_pages in",
		},
		{
			name:    "no nr_free_pages field ",
			content: "numa_hit 1000",
			wantErr: true,
			errMsg:  "failed to find nr_free_pages in",
		},
		{
			name:    "fields not equal 2",
			content: "nr_free_pages 10000 10",
			wantErr: true,
			errMsg:  "invalid line",
		},
		{
			name:    "fields value invalid",
			content: "nr_free_pages test",
			wantErr: true,
			errMsg:  "invalid line",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			generateNodeVMStatFile(t, tmpDir, 0, tt.content)
			_, err := getNumasFreeMemRatio(tmpDir, []int{0})
			if (err != nil) != tt.wantErr {
				t.Errorf("test name: %s for getNumasFreeMemRatio error = %v, wantErr %v", tt.name, err, tt.wantErr)
				return
			}

			assert.Assert(t, strings.Contains(err.Error(), tt.errMsg))
		})
	}
}

func TestGetProcessPageStats(t *testing.T) {
	tests := []struct {
		name    string
		pid     int
		content string
		wantErr bool
		want    *smapsInfo
	}{
		{
			name: "pid with two vma",
			pid:  12345,
			content: `
00400000-00404000 r--p 00000000 103:05 81289872
Size: 100 kB
KernelPageSize: 10 kB
Rss: 50 kB
Pss: 4 kB
VmFlags: rd mr mw me dw sd

01630000-016a6000 r--p 00000000 103:05 81289872
Size: 200 kB
KernelPageSize: 20 kB
Rss: 100 kB
Pss: 20 kB
VmFlags: rd mr mw me dw sd
`,
			want: &smapsInfo{
				totalRss: 150 * 1024,
				vmas: []vmaInfo{
					{
						start:       4194304,
						end:         4210688,
						rss:         50 * 1024,
						pageSize:    10 * 1024,
						virtualSize: 100 * 1024,
					},
					{
						start:       23265280,
						end:         23748608,
						rss:         100 * 1024,
						pageSize:    20 * 1024,
						virtualSize: 200 * 1024,
					},
				},
			},
		},
		{
			name: "pid with one vma ",
			pid:  12345,
			content: `
00400000-00404000 r--p 00000000 103:05 81289872
Size: 100 kB
KernelPageSize: 10 kB
Rss: 50 kB
Pss: 4 kB
VmFlags: rd mr mw me dw sd

Size: 200 kB
KernelPageSize: 20 kB
Rss: 100 kB
Pss: 20 kB
VmFlags: rd mr mw me dw sd
`,
			want: &smapsInfo{
				totalRss: 50 * 1024,
				vmas: []vmaInfo{
					{
						start:       4194304,
						end:         4210688,
						rss:         50 * 1024,
						pageSize:    10 * 1024,
						virtualSize: 100 * 1024,
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			generatePidSmapsFile(t, tmpDir, tt.pid, tt.content)
			got, err := getProcessPageStats(tmpDir, tt.pid)

			if (err != nil) != tt.wantErr {
				t.Errorf("test name: %s for getProcessPageStats error = %v, wantErr %v", tt.name, err, tt.wantErr)
				return
			}

			if !reflect.DeepEqual(tt.want, got) {
				t.Errorf("test name: %s for getProcessPageStats got = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestGetProcessPageStats_SmapsFileNotFound(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
		errMsg  string
	}{
		{
			name:    "read smaps file failed",
			wantErr: true,
			errMsg:  "failed to ReadLines",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			_, err := getProcessPageStats(tmpDir, 12345)
			if (err != nil) != tt.wantErr {
				t.Errorf("test name: %s for getProcessPageStats error = %v, wantErr %v", tt.name, err, tt.wantErr)
				return
			}

			assert.Assert(t, strings.Contains(err.Error(), tt.errMsg))
		})
	}
}

func TestGetProcessPageStats_ReadSmapsFailed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		wantErr bool
		result  *smapsInfo
	}{
		{
			name:    "empty file",
			content: "",
			wantErr: false,
			result:  &smapsInfo{},
		},
		{
			name: "no start and end",
			content: `
Size: 200 kB
KernelPageSize: 20 kB
Rss: 100 kB
Pss: 20 kB
VmFlags: rd mr mw me dw sd
`,
			wantErr: false,
			result:  &smapsInfo{},
		},
		{
			name: "no rss",
			content: `
00400000-00404000 r--p 00000000 103:05 81289872
Size: 100 kB
KernelPageSize: 10 kB
Pss: 4 kB
VmFlags: rd mr mw me dw sd
`,
			wantErr: false,
			result:  &smapsInfo{},
		},
		{
			name: "no VmFlags",
			content: `
00400000-00404000 r--p 00000000 103:05 81289872
Size: 100 kB
KernelPageSize: 10 kB
Rss: 100 kB
Pss: 4 kB
`,
			wantErr: false,
			result:  &smapsInfo{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			generatePidSmapsFile(t, tmpDir, 12345, tt.content)
			got, err := getProcessPageStats(tmpDir, 12345)
			if (err != nil) != tt.wantErr {
				t.Errorf("test name: %s for getProcessPageStats error = %v, wantErr %v", tt.name, err, tt.wantErr)
				return
			}

			reflect.DeepEqual(tt.result, got)
		})
	}
}

func TestMovePagesForProcess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		pid       int
		srcNumas  []int
		destNumas []int
		content   string
		wantErr   bool
		errMsg    string
	}{
		{
			name:      "process rss small, skip migrate",
			pid:       12345,
			srcNumas:  []int{0},
			destNumas: []int{1},
			content: `
00400000-00404000 r--p 00000000 103:05 81289872
Size: 100 kB
KernelPageSize: 10 kB
Rss: 1024 kB
Pss: 4 kB
VmFlags: rd mr mw me dw sd
`,
			wantErr: false,
		},
		{
			name:      "invalid srcNums bitmask",
			pid:       12345,
			srcNumas:  []int{0, -1, 65},
			destNumas: []int{1},
			content: `
00400000-00404000 r--p 00000000 103:05 81289872
Size: 409600 kB
KernelPageSize: 10 kB
Rss: 204900 kB
Pss: 4 kB
VmFlags: rd mr mw me dw sd
`,
			wantErr: true,
			errMsg:  "failed to NewBitMask allowd numas",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			generatePidSmapsFile(t, tmpDir, tt.pid, tt.content)
			err := MovePagesForProcess(context.Background(), tmpDir, tt.pid, tt.srcNumas, tt.destNumas)

			if (err != nil) != tt.wantErr {
				t.Errorf("test name: %s for getProcessPageStats error = %v, wantErr %v", tt.name, err, tt.wantErr)
				return
			}

			if err != nil {
				assert.Assert(t, strings.Contains(err.Error(), tt.errMsg))
			}
		})
	}
}

func TestMovePagesForContainer(t *testing.T) {
	err := MovePagesForContainer(context.Background(), "pod123", "container123", machine.NewCPUSet([]int{1, 2}...), machine.NewCPUSet([]int{1, 2}...))
	assert.NilError(t, err)
}

func generateNodeVMStatFile(t *testing.T, dir string, nodeId int, content string) {
	nodeDir := filepath.Join(dir, fmt.Sprintf("node%d", nodeId))
	err := os.MkdirAll(nodeDir, 0777)
	require.NoError(t, err)

	vmStatFile := filepath.Join(nodeDir, "vmstat")

	err = os.WriteFile(vmStatFile, []byte(content), 0777)
	require.NoError(t, err)
	return
}

func generatePidSmapsFile(t *testing.T, dir string, pid int, content string) {
	nodeDir := filepath.Join(dir, fmt.Sprintf("%d", pid))
	err := os.MkdirAll(nodeDir, 0777)
	require.NoError(t, err)

	vmStatFile := filepath.Join(nodeDir, "smaps")

	err = os.WriteFile(vmStatFile, []byte(content), 0777)
	require.NoError(t, err)
	return
}
