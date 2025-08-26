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

package process

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"k8s.io/apimachinery/pkg/util/sets"

	procfsm "github.com/kubewharf/katalyst-core/pkg/util/procfs/manager"
)

const PF_KTHREAD = 0x00200000

type NameSpaceKind string

const (
	NetNS    NameSpaceKind = "net"
	PidNS    NameSpaceKind = "pid"
	MntNS    NameSpaceKind = "mnt"
	UserNS   NameSpaceKind = "user"
	UtsNS    NameSpaceKind = "uts"
	IpcNS    NameSpaceKind = "ipc"
	CgroupNS NameSpaceKind = "cgroup"
)

// CPUSetParse constructs an integer cpu set from a Linux CPU list formatted string.
// See: http://man7.org/linux/man-pages/man7/cpuset.7.html#FORMATS
func CPUSetParse(s string) (sets.Int, error) {
	if s == "" {
		return sets.Int{}, nil
	}

	b := sets.Int{}
	ranges := strings.Split(s, ",")

	for _, r := range ranges {
		boundaries := strings.Split(r, "-")
		if len(boundaries) == 1 {
			elem, err := strconv.Atoi(boundaries[0])
			if err != nil {
				return sets.Int{}, fmt.Errorf("parse index 0 of 1 boundaries failed: %w", err)
			}

			b.Insert(elem)
		} else if len(boundaries) == 2 {
			start, err := strconv.Atoi(boundaries[0])
			if err != nil {
				return sets.Int{}, fmt.Errorf("parse index 0 of 2 boundaries failed: %w", err)
			}

			end, err := strconv.Atoi(boundaries[1])
			if err != nil {
				return sets.Int{}, fmt.Errorf("parse index 1 of 2 boundaries failed: %w", err)
			}

			for e := start; e <= end; e++ {
				b.Insert(e)
			}
		}
	}

	return b, nil
}

// IsCommandInDState checks if the specified command is in the D (uninterruptible sleep) state.
func IsCommandInDState(command string) bool {
	// Execute the command: ps -aux | grep " D" | grep <command> | grep -v grep
	cmd := exec.Command("sh", "-c", "ps -aux | grep ' D' | grep "+command+" | grep -v grep")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()

	// If the command runs successfully and output is not empty, the process is in D state
	if err == nil && strings.TrimSpace(out.String()) != "" {
		return true
	}
	return false
}

func IsKernelThread(pid int) (bool, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false, fmt.Errorf("failed to ReadLines /proc/%d/stat, err %v", pid, err)
	}

	pidStat := strings.TrimSuffix(strings.TrimSpace(string(b)), "\n")
	cols := strings.Fields(pidStat)
	taskFlags, err := strconv.Atoi(cols[8])
	if err != nil {
		return false, fmt.Errorf("failed to Atoi(%s) 9th col of /proc/%d/stat, err %v", cols[8], pid, err)
	}

	// https://stackoverflow.com/questions/61935596/how-to-identify-a-thread-is-a-kernel-thread-or-not-through-bash
	if taskFlags&PF_KTHREAD == 0 {
		return false, nil
	}

	return true, nil
}

// ListKsoftirqdProcesses list ksoftirqd processes, return value: cpu id as map key, ksoftirq pid as value
func ListKsoftirqdProcesses() (map[int64]int, error) {
	dirEnts, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("failed to ReadDir(/proc), err %v", err)
	}

	ksoftirqds := make(map[int64]int)
	for _, de := range dirEnts {
		if !de.IsDir() {
			continue
		}

		pid, err := strconv.Atoi(de.Name())
		if err != nil || pid < 0 {
			continue
		}

		if b, err := IsKernelThread(pid); err != nil || !b {
			continue
		}

		procCommPath := filepath.Join("/proc", de.Name(), "comm")
		b, err := os.ReadFile(procCommPath)
		if err != nil {
			continue
		}
		comm := strings.TrimSuffix(strings.TrimSpace(string(b)), "\n")

		cols := strings.Split(comm, "/")
		if len(cols) != 2 {
			continue
		}

		if cols[0] != "ksoftirqd" {
			continue
		}

		cpuID, err := strconv.ParseInt(cols[1], 10, 64)
		if err != nil || cpuID < 0 {
			continue
		}

		ksoftirqds[cpuID] = pid
	}

	return ksoftirqds, nil
}

// CheckIfProcCommRunning check if there is a running process with specified proc comm
func CheckIfProcCommRunning(procComm string) (bool, error) {
	dirEnts, err := os.ReadDir("/proc")
	if err != nil {
		return false, fmt.Errorf("failed to ReadDir(/proc), err %v", err)
	}

	for _, de := range dirEnts {
		if !de.IsDir() {
			continue
		}

		pid, err := strconv.Atoi(de.Name())
		if err != nil || pid < 0 {
			continue
		}

		if pid <= 0 {
			continue
		}

		pidComm, err := procfsm.GetPidComm(pid)
		if err != nil {
			continue
		}
		comm := strings.TrimSuffix(pidComm, "\n")

		if comm == procComm {
			return true, nil
		}
	}

	return false, nil
}

func GetProcessNameSpaceInode(pid int, ns NameSpaceKind) (uint64, error) {
	processNSPath := fmt.Sprintf("/proc/%d/ns/%s", pid, ns)
	link, err := os.Readlink(processNSPath)
	if err != nil {
		return 0, fmt.Errorf("failed to Readlink(%s), err %v", link, err)
	}

	// Find the start and end positions of the inode number within the link
	start := strings.Index(link, "[")
	end := strings.Index(link, "]")
	if start == -1 || end == -1 || start+1 >= end {
		return 0, fmt.Errorf("unable to parse the inode number from the link: %s", link)
	}

	// Extract the inode number
	inodeStr := link[start+1 : end]

	inode, err := strconv.ParseUint(inodeStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("unable to parse the inode string %s, err %v", inodeStr, err)
	}

	return inode, nil
}

func SetProcessNice(pid int, niceness int) error {
	if pid < 0 {
		return fmt.Errorf("invalid pid %d", pid)
	}

	if niceness < -20 {
		niceness = -20
	} else if niceness > 19 {
		niceness = 19
	}

	if err := syscall.Setpriority(syscall.PRIO_PROCESS, pid, niceness); err != nil {
		return fmt.Errorf("failed to Setpriority for pid %d, nice %d , err %v", pid, niceness, err)
	}

	return nil
}

func GetProcessNice(pid int) (int, error) {
	if pid < 0 {
		return 0, fmt.Errorf("invalid pid %d", pid)
	}

	niceness, err := syscall.Getpriority(syscall.PRIO_PROCESS, pid)
	if err != nil {
		return 0, fmt.Errorf("failed to Getpriority for pid %d: %v", pid, err)
	}

	// Within the kernel, nice values are actually represented using the corresponding range 40..1 (since negative numbers are error codes)
	// and these are the values employed by the setpriority() and getpriority() system calls. The glibc wrapper functions for these system calls
	// handle the translations between the user-land and kernel representations of the nice value according to the formula unice = 20 - knice.
	// reference https://linux.die.net/man/2/setpriority
	return 20 - niceness, nil
}
