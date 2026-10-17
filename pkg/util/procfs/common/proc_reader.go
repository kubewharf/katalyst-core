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
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	utilfs "github.com/kubewharf/katalyst-core/pkg/util/fs"
)

// PF_KTHREAD mirrors the Linux task_struct PF_KTHREAD flag (bit 21 of the task
// flags field exposed as the 9th column of /proc/<pid>/stat). A task with this
// bit set is a kernel thread. See include/linux/sched.h and
// https://stackoverflow.com/questions/61935596.
const PF_KTHREAD = 0x00200000

// ProcInfo is a coarse snapshot of one PID's classification.
type ProcInfo struct {
	PID       int
	Comm      string
	IsKThread bool
	PPID      int
}

// ProcReader enumerates and classifies processes from a procfs mount.
type ProcReader interface {
	// ListPIDs returns the set of numeric PIDs currently present.
	ListPIDs() ([]int, error)
	// ReadProc returns a ProcInfo for pid.
	ReadProc(pid int) (ProcInfo, error)
	// SchedSetaffinity pins pid to cpus via the sched_setaffinity(2) syscall.
	SchedSetaffinity(pid int, cpus []int) error
}

type osProcReader struct {
	fs       utilfs.FS
	procRoot string
}

// NewProcReader returns a ProcReader rooted at procRoot.
func NewProcReader(fs utilfs.FS, procRoot string) ProcReader {
	return &osProcReader{fs: fs, procRoot: procRoot}
}

func (p *osProcReader) ListPIDs() ([]int, error) {
	entries, err := p.fs.ReadDir(p.procRoot)
	if err != nil {
		return nil, fmt.Errorf("readdir %s: %w", p.procRoot, err)
	}

	out := make([]int, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid <= 0 {
			continue
		}
		out = append(out, pid)
	}
	return out, nil
}

func (p *osProcReader) ReadProc(pid int) (ProcInfo, error) {
	base := filepath.Join(p.procRoot, strconv.Itoa(pid))
	comm, err := p.fs.ReadFile(filepath.Join(base, "comm"))
	if err != nil {
		return ProcInfo{}, fmt.Errorf("read comm: %w", err)
	}
	stat, err := p.fs.ReadFile(filepath.Join(base, "stat"))
	if err != nil {
		return ProcInfo{}, fmt.Errorf("read stat: %w", err)
	}
	ppid, flags, err := parseStatFields(stat)
	if err != nil {
		return ProcInfo{}, fmt.Errorf("parse stat for pid %d: %w", pid, err)
	}

	return ProcInfo{
		PID:       pid,
		Comm:      strings.TrimRight(string(comm), "\r\n"),
		PPID:      ppid,
		IsKThread: flags&PF_KTHREAD != 0,
	}, nil
}

func (p *osProcReader) SchedSetaffinity(pid int, cpus []int) error {
	if pid <= 0 {
		return fmt.Errorf("sched_setaffinity: invalid pid %d", pid)
	}
	if len(cpus) == 0 {
		return fmt.Errorf("sched_setaffinity: refusing to pin pid %d to empty cpuset", pid)
	}
	return schedSetaffinity(pid, cpus)
}

func parseStatFields(stat []byte) (ppid int, flags uint64, err error) {
	s := string(stat)
	// comm (field 2) is wrapped in parentheses and may itself contain
	// spaces or parens; split by locating the LAST ')' so the remaining
	// fields align with proc(5) starting at state (field 3).
	rp := strings.LastIndexByte(s, ')')
	if rp < 0 || rp+2 >= len(s) {
		return 0, 0, fmt.Errorf("invalid stat: missing closing paren or truncated tail")
	}
	// After the last ')', fields are: state, ppid, pgrp, session,
	// tty_nr, tpgid, flags, ... (indices 0..6 respectively). We need
	// at least 7 fields to read the task flags column.
	rest := strings.Fields(s[rp+2:])
	if len(rest) < 7 {
		return 0, 0, fmt.Errorf("invalid stat: only %d fields after paren, want >=7", len(rest))
	}
	ppid, err = strconv.Atoi(rest[1])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid stat ppid %q: %w", rest[1], err)
	}
	flags, err = strconv.ParseUint(rest[6], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid stat flags %q: %w", rest[6], err)
	}
	return ppid, flags, nil
}
