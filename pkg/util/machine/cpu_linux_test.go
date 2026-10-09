//go:build linux
// +build linux

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

package machine

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/cpuid/v2"
	"github.com/stretchr/testify/assert"
)

// writeSysFile writes content to path, creating parent directories. It is used
// to materialize a fake sysfs/proc tree inside t.TempDir() so tests never touch
// the real host filesystem and stay parallel-safe.
func writeSysFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func Test_GetCPUInfoWithTopo(t *testing.T) {
	t.Parallel()

	t.Run("intel topology built from fake sysfs", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeSys := filepath.Join(root, "node")

		writeSysFile(t, filepath.Join(nodeSys, "node0", "cpulist"), "0-3\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "physical_package_id"), "0\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "0,2\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu1", "topology", "thread_siblings_list"), "1,3\n")
		for _, cpu := range []string{"cpu0", "cpu1", "cpu2", "cpu3"} {
			writeSysFile(t, filepath.Join(cpuSys, cpu, "online"), "1\n")
		}

		info, err := getCPUInfoWithTopo(cpuSys, nodeSys, cpuid.Intel)
		assert.NoError(t, err)
		assert.NotNil(t, info)
		assert.Equal(t, cpuid.Intel, info.CPUVendor)
		assert.Len(t, info.Sockets, 1)
		assert.NotNil(t, info.Sockets[0])
		assert.Equal(t, []int{0}, info.Sockets[0].NumaIDs)
		assert.Equal(t, []int64{0, 1, 2, 3}, info.Sockets[0].CPUs)
		assert.Equal(t, 0, info.CPU2Socket[0])
		assert.Equal(t, 0, info.CPU2Socket[3])
		assert.True(t, info.CPUOnline[0])
		assert.True(t, info.CPUOnline[3])
	})

	t.Run("amd topology built from fake sysfs", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeSys := filepath.Join(root, "node")

		writeSysFile(t, filepath.Join(nodeSys, "node0", "cpulist"), "0,8\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "cache", "index3", "shared_cpu_list"), "0-1,8-9\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "0,8\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu1", "topology", "thread_siblings_list"), "1,9\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "physical_package_id"), "0\n")
		for _, cpu := range []string{"cpu1", "cpu8", "cpu9"} {
			writeSysFile(t, filepath.Join(cpuSys, cpu, "online"), "1\n")
		}

		info, err := getCPUInfoWithTopo(cpuSys, nodeSys, cpuid.AMD)
		assert.NoError(t, err)
		assert.NotNil(t, info)
		assert.Equal(t, cpuid.AMD, info.CPUVendor)
		assert.Len(t, info.Sockets, 1)
		assert.NotNil(t, info.Sockets[0])
		assert.Equal(t, []int{0}, info.Sockets[0].NumaIDs)
		assert.Equal(t, []int64{0, 1, 8, 9}, info.Sockets[0].CPUs)
		assert.Equal(t, 0, info.CPU2Socket[9])
		assert.True(t, info.CPUOnline[8])
	})

	t.Run("unsupported vendor returns nil", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		info, err := getCPUInfoWithTopo(filepath.Join(root, "cpu"), filepath.Join(root, "node"), cpuid.VendorUnknown)
		assert.NoError(t, err)
		assert.Nil(t, info)
	})

	t.Run("failed to read node directory", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		info, err := getCPUInfoWithTopo(filepath.Join(root, "cpu"), filepath.Join(root, "no-such-node"), cpuid.Intel)
		assert.Nil(t, info)
		assert.Error(t, err)
	})

	t.Run("offline cpu exists", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeSys := filepath.Join(root, "node")

		writeSysFile(t, filepath.Join(nodeSys, "node0", "cpulist"), "0,1\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "physical_package_id"), "0\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "0,1\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu1", "online"), "0\n")

		info, err := getCPUInfoWithTopo(cpuSys, nodeSys, cpuid.Intel)
		assert.Nil(t, info)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "offline cpu 1 exists in /sys/devices/system/node/nodeX/cpu_list")
	})

	t.Run("failed to obtain cpu package id", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeSys := filepath.Join(root, "node")

		writeSysFile(t, filepath.Join(nodeSys, "node0", "cpulist"), "0\n")
		// cpu0/topology exists but physical_package_id is absent so the read fails.
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "0\n")

		info, err := getCPUInfoWithTopo(cpuSys, nodeSys, cpuid.Intel)
		assert.Nil(t, info)
		assert.Error(t, err)
	})
}

func Test_getAMDNumaTopo(t *testing.T) {
	t.Parallel()

	t.Run("two ccds built from fake cache siblings", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeCPUListFile := filepath.Join(root, "node0_cpulist")
		writeSysFile(t, nodeCPUListFile, "0,2\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "cache", "index3", "shared_cpu_list"), "0,1\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "0,1\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu2", "cache", "index3", "shared_cpu_list"), "2,3\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu2", "topology", "thread_siblings_list"), "2,3\n")

		numa, err := getAMDNumaTopo(cpuSys, nodeCPUListFile)
		assert.NoError(t, err)
		assert.NotNil(t, numa)
		expected := &AMDNuma{
			CCDs: []*LLCDomain{
				{PhyCores: []PhyCore{{CPUs: []int64{0, 1}}}},
				{PhyCores: []PhyCore{{CPUs: []int64{2, 3}}}},
			},
		}
		assert.Equal(t, expected, numa)
	})

	t.Run("already-parsed cpus are not revisited", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeCPUListFile := filepath.Join(root, "node0_cpulist")
		writeSysFile(t, nodeCPUListFile, "0,1\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "cache", "index3", "shared_cpu_list"), "0,1\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "0,1\n")

		numa, err := getAMDNumaTopo(cpuSys, nodeCPUListFile)
		assert.NoError(t, err)
		assert.NotNil(t, numa)
		assert.Len(t, numa.CCDs, 1)
		expected := &AMDNuma{
			CCDs: []*LLCDomain{
				{PhyCores: []PhyCore{{CPUs: []int64{0, 1}}}},
			},
		}
		assert.Equal(t, expected, numa)
	})

	t.Run("failed to parse node cpu list", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		numa, err := getAMDNumaTopo(filepath.Join(root, "cpu"), filepath.Join(root, "missing-cpulist"))
		assert.Nil(t, numa)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to ParseLinuxListFormatFromFile")
	})

	t.Run("getLLCDomain propagates error", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeCPUListFile := filepath.Join(root, "node0_cpulist")
		writeSysFile(t, nodeCPUListFile, "0\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "cache", "index3", "shared_cpu_list"), "0\n")
		// empty thread_siblings_list makes getLLCDomain reject a zero-cpu core.
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "")

		numa, err := getAMDNumaTopo(cpuSys, nodeCPUListFile)
		assert.Nil(t, numa)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to getLLCDomain")
	})

	t.Run("l3 shared cpu list missing yields no ccd", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeCPUListFile := filepath.Join(root, "node0_cpulist")
		writeSysFile(t, nodeCPUListFile, "0\n")
		// cpu0/cache/index3/shared_cpu_list deliberately absent.

		numa, err := getAMDNumaTopo(cpuSys, nodeCPUListFile)
		assert.NoError(t, err)
		assert.NotNil(t, numa)
		assert.Empty(t, numa.CCDs)
	})
}

func TestGetNumaPackageID(t *testing.T) {
	t.Parallel()

	t.Run("cpulist not exists", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeSys := filepath.Join(root, "node")
		// node0/cpulist absent
		_ = os.MkdirAll(filepath.Join(nodeSys, "node0"), 0o755)

		id, err := getNumaPackageID(cpuSys, nodeSys, 0)
		assert.Error(t, err)
		assert.Equal(t, -1, id)
		assert.Contains(t, err.Error(), "not exists")
	})

	t.Run("failed to parse cpulist", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeSys := filepath.Join(root, "node")
		writeSysFile(t, filepath.Join(nodeSys, "node0", "cpulist"), "not-a-list\n")

		id, err := getNumaPackageID(cpuSys, nodeSys, 0)
		assert.Error(t, err)
		assert.Equal(t, -1, id)
		assert.Contains(t, err.Error(), "failed to ParseLinuxListFormatFromFile")
	})

	t.Run("cpulist non-empty resolves package id", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeSys := filepath.Join(root, "node")
		writeSysFile(t, filepath.Join(nodeSys, "node0", "cpulist"), "4\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu4", "topology", "physical_package_id"), "1\n")

		id, err := getNumaPackageID(cpuSys, nodeSys, 0)
		assert.NoError(t, err)
		assert.Equal(t, 1, id)
	})

	t.Run("cpulist non-empty but package id read fails", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeSys := filepath.Join(root, "node")
		writeSysFile(t, filepath.Join(nodeSys, "node0", "cpulist"), "4\n")
		// cpu4/topology exists but physical_package_id absent.
		writeSysFile(t, filepath.Join(cpuSys, "cpu4", "topology", "physical_package_id.tmp"), "")

		id, err := getNumaPackageID(cpuSys, nodeSys, 0)
		assert.Error(t, err)
		assert.Equal(t, -1, id)
		assert.Contains(t, err.Error(), "failed to GetCPUPackageID")
	})

	t.Run("empty cpulist and socket count fails", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		nodeSys := filepath.Join(root, "node")
		writeSysFile(t, filepath.Join(nodeSys, "node0", "cpulist"), "")
		// cpuSys points at a non-existent directory so getSocketCount fails.
		cpuSys := filepath.Join(root, "no-such-cpu")

		id, err := getNumaPackageID(cpuSys, nodeSys, 0)
		assert.Error(t, err)
		assert.Equal(t, -1, id)
		assert.Contains(t, err.Error(), "failed to GetSocketCount")
	})

	t.Run("empty cpulist and socket count is zero", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeSys := filepath.Join(root, "node")
		writeSysFile(t, filepath.Join(nodeSys, "node0", "cpulist"), "")
		// cpuSys exists but holds no cpu* directory.
		_ = os.MkdirAll(cpuSys, 0o755)

		id, err := getNumaPackageID(cpuSys, nodeSys, 0)
		assert.Error(t, err)
		assert.Equal(t, -1, id)
		assert.Contains(t, err.Error(), "socket count is 0")
	})

	t.Run("empty cpulist and single socket resolves to 0", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeSys := filepath.Join(root, "node")
		writeSysFile(t, filepath.Join(nodeSys, "node0", "cpulist"), "")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "physical_package_id"), "0\n")

		id, err := getNumaPackageID(cpuSys, nodeSys, 0)
		assert.NoError(t, err)
		assert.Equal(t, 0, id)
	})

	t.Run("empty cpulist and multi socket divides by node count", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeSys := filepath.Join(root, "node")
		writeSysFile(t, filepath.Join(nodeSys, "node0", "cpulist"), "")
		writeSysFile(t, filepath.Join(nodeSys, "node1", "cpulist"), "")
		writeSysFile(t, filepath.Join(nodeSys, "node2", "cpulist"), "")
		writeSysFile(t, filepath.Join(nodeSys, "node3", "cpulist"), "")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "physical_package_id"), "0\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu100", "topology", "physical_package_id"), "1\n")

		// socketCount=2, numaCount=4 -> nodeCountPerSocket=2, nodeID=3 -> 3/2=1
		id, err := getNumaPackageID(cpuSys, nodeSys, 3)
		assert.NoError(t, err)
		assert.Equal(t, 1, id)
	})
}

func Test_GetCPUOnlineStatus(t *testing.T) {
	t.Parallel()

	t.Run("cpu0 always online", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		online, err := getCPUOnlineStatus(filepath.Join(root, "cpu"), 0)
		assert.NoError(t, err)
		assert.True(t, online)
	})

	t.Run("cpu dir missing errors", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		_ = os.MkdirAll(cpuSys, 0o755)

		online, err := getCPUOnlineStatus(cpuSys, 99)
		assert.False(t, online)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "cpu 99 not exists")
	})

	t.Run("missing online file defaults to true", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		// cpu1 dir exists but no online file.
		_ = os.MkdirAll(filepath.Join(cpuSys, "cpu1"), 0o755)

		online, err := getCPUOnlineStatus(cpuSys, 1)
		assert.NoError(t, err)
		assert.True(t, online)
	})

	t.Run("online content 1", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		writeSysFile(t, filepath.Join(cpuSys, "cpu2", "online"), "1\n")

		online, err := getCPUOnlineStatus(cpuSys, 2)
		assert.NoError(t, err)
		assert.True(t, online)
	})

	t.Run("online content 0", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		writeSysFile(t, filepath.Join(cpuSys, "cpu3", "online"), "0\n")

		online, err := getCPUOnlineStatus(cpuSys, 3)
		assert.NoError(t, err)
		assert.False(t, online)
	})

	t.Run("non-number content errors", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		writeSysFile(t, filepath.Join(cpuSys, "cpu5", "online"), "invalid-data\n")

		online, err := getCPUOnlineStatus(cpuSys, 5)
		assert.False(t, online)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid syntax")
	})
}

func Test_getLLCDomain(t *testing.T) {
	t.Parallel()

	t.Run("build two physical cores", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		cpuListFile := filepath.Join(root, "cpu_list")
		writeSysFile(t, cpuListFile, "0,2\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "0,1\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu2", "topology", "thread_siblings_list"), "2,3\n")

		domain, err := getLLCDomain(cpuSys, cpuListFile)
		assert.NoError(t, err)
		assert.NotNil(t, domain)
		assert.Len(t, domain.PhyCores, 2)
		expected := &LLCDomain{
			PhyCores: []PhyCore{
				{CPUs: []int64{0, 1}},
				{CPUs: []int64{2, 3}},
			},
		}
		assert.Equal(t, expected, domain)
	})

	t.Run("main cpu list parse fails", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")

		domain, err := getLLCDomain(cpuSys, filepath.Join(root, "missing-cpu-list"))
		assert.Nil(t, domain)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to ParseLinuxListFormatFromFile")
	})

	t.Run("missing topology file skips that cpu", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		cpuListFile := filepath.Join(root, "cpu_list")
		writeSysFile(t, cpuListFile, "0,2\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "0,1\n")
		// cpu2/topology/thread_siblings_list deliberately absent.

		domain, err := getLLCDomain(cpuSys, cpuListFile)
		assert.NoError(t, err)
		assert.NotNil(t, domain)
		assert.Len(t, domain.PhyCores, 1)
		expected := &LLCDomain{
			PhyCores: []PhyCore{
				{CPUs: []int64{0, 1}},
			},
		}
		assert.Equal(t, expected, domain)
	})

	t.Run("topology parse failure propagates", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		cpuListFile := filepath.Join(root, "cpu_list")
		writeSysFile(t, cpuListFile, "0\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "bad-list\n")

		domain, err := getLLCDomain(cpuSys, cpuListFile)
		assert.Nil(t, domain)
		assert.Error(t, err)
	})

	t.Run("empty sibling list rejected", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		cpuListFile := filepath.Join(root, "cpu_list")
		writeSysFile(t, cpuListFile, "0\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "")

		domain, err := getLLCDomain(cpuSys, cpuListFile)
		assert.Nil(t, domain)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "has 0 cpu")
	})

	t.Run("empty main cpu list yields empty domain", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		cpuListFile := filepath.Join(root, "cpu_list")
		writeSysFile(t, cpuListFile, "")

		domain, err := getLLCDomain(cpuSys, cpuListFile)
		assert.NoError(t, err)
		assert.NotNil(t, domain)
		assert.Empty(t, domain.PhyCores)
	})
}

func Test_GetCPUPackageID(t *testing.T) {
	t.Parallel()

	t.Run("reads physical package id", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "physical_package_id"), "1\n")

		pkgID, err := getCPUPackageID(cpuSys, 0)
		assert.NoError(t, err)
		assert.Equal(t, 1, pkgID)
	})

	t.Run("topology dir missing", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		_ = os.MkdirAll(cpuSys, 0o755)

		pkgID, err := getCPUPackageID(cpuSys, 1)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "is not exist")
		assert.Equal(t, -1, pkgID)
	})

	t.Run("physical package id file unreadable", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		// topology dir exists but physical_package_id absent.
		_ = os.MkdirAll(filepath.Join(cpuSys, "cpu2", "topology"), 0o755)

		pkgID, err := getCPUPackageID(cpuSys, 2)
		assert.Error(t, err)
		assert.Equal(t, -1, pkgID)
	})

	t.Run("invalid package id content", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		writeSysFile(t, filepath.Join(cpuSys, "cpu3", "topology", "physical_package_id"), "invalid-id\n")

		pkgID, err := getCPUPackageID(cpuSys, 3)
		assert.Error(t, err)
		assert.Equal(t, 0, pkgID)
	})
}

func Test_GetSocketCount(t *testing.T) {
	t.Parallel()

	t.Run("counts two physical packages", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "physical_package_id"), "0\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu1", "topology", "physical_package_id"), "0\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu2", "topology", "physical_package_id"), "1\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu3", "topology", "physical_package_id"), "1\n")
		// non-cpu directory and a plain file are ignored.
		_ = os.MkdirAll(filepath.Join(cpuSys, "not_a_cpu"), 0o755)
		_ = os.WriteFile(filepath.Join(cpuSys, "a_file"), nil, 0o644)

		count, err := getSocketCount(cpuSys)
		assert.NoError(t, err)
		assert.Equal(t, 2, count)
	})

	t.Run("missing cpu root errors", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		count, err := getSocketCount(filepath.Join(root, "no-such-cpu"))
		assert.Equal(t, -1, count)
		assert.Error(t, err)
	})

	t.Run("unreadable package id errors", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		// cpu0/topology exists but physical_package_id absent.
		_ = os.MkdirAll(filepath.Join(cpuSys, "cpu0", "topology"), 0o755)

		count, err := getSocketCount(cpuSys)
		assert.Equal(t, -1, count)
		assert.Error(t, err)
	})

	t.Run("cpu without topology is skipped", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "physical_package_id"), "0\n")
		// cpu1 dir exists but has no topology subdir.
		_ = os.MkdirAll(filepath.Join(cpuSys, "cpu1"), 0o755)

		count, err := getSocketCount(cpuSys)
		assert.NoError(t, err)
		assert.Equal(t, 1, count)
	})

	t.Run("no cpu-prefixed dirs yields zero", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		_ = os.MkdirAll(filepath.Join(cpuSys, "node0"), 0o755)
		_ = os.WriteFile(filepath.Join(cpuSys, "some_file"), nil, 0o644)

		count, err := getSocketCount(cpuSys)
		assert.NoError(t, err)
		assert.Equal(t, 0, count)
	})
}

func Test_getIntelNumaTopo(t *testing.T) {
	t.Parallel()

	t.Run("normal two cores", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeCPUListFile := filepath.Join(root, "node0_cpulist")
		writeSysFile(t, nodeCPUListFile, "0,4\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "0,1\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu4", "topology", "thread_siblings_list"), "4,5\n")

		domain, err := getIntelNumaTopo(cpuSys, nodeCPUListFile)
		assert.NoError(t, err)
		assert.NotNil(t, domain)
		expected := &LLCDomain{
			PhyCores: []PhyCore{
				{CPUs: []int64{0, 1}},
				{CPUs: []int64{4, 5}},
			},
		}
		assert.Equal(t, expected, domain)
	})

	t.Run("node cpu list parse fails", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")

		domain, err := getIntelNumaTopo(cpuSys, filepath.Join(root, "missing-cpulist"))
		assert.Nil(t, domain)
		assert.Error(t, err)
	})

	t.Run("missing topology file skips that core", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeCPUListFile := filepath.Join(root, "node0_cpulist")
		writeSysFile(t, nodeCPUListFile, "0,4\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "0,1\n")
		// cpu4/topology/thread_siblings_list deliberately absent.

		domain, err := getIntelNumaTopo(cpuSys, nodeCPUListFile)
		assert.NoError(t, err)
		assert.NotNil(t, domain)
		expected := &LLCDomain{
			PhyCores: []PhyCore{
				{CPUs: []int64{0, 1}},
			},
		}
		assert.Equal(t, expected, domain)
	})

	t.Run("topology parse failure propagates", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeCPUListFile := filepath.Join(root, "node0_cpulist")
		writeSysFile(t, nodeCPUListFile, "0\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "bad-list\n")

		domain, err := getIntelNumaTopo(cpuSys, nodeCPUListFile)
		assert.Nil(t, domain)
		assert.Error(t, err)
	})

	t.Run("empty sibling list rejected", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuSys := filepath.Join(root, "cpu")
		nodeCPUListFile := filepath.Join(root, "node0_cpulist")
		writeSysFile(t, nodeCPUListFile, "0\n")
		writeSysFile(t, filepath.Join(cpuSys, "cpu0", "topology", "thread_siblings_list"), "")

		domain, err := getIntelNumaTopo(cpuSys, nodeCPUListFile)
		assert.Nil(t, domain)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "has 0 cpu")
	})
}

func TestCollectCpuStats(t *testing.T) {
	t.Parallel()

	t.Run("parses cpu stats", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		statFile := filepath.Join(root, "stat")
		content := `cpu0 100 10 200 300 40 50 60 70 80 90
cpu1 101 11 201 301 41 51 61 71
cpu2 102 12 202 302 42 52 62
cpu  1000 100 2000 3000 400 500 600 700 800 900
other_line 123 456
`
		writeSysFile(t, statFile, content)

		stats, err := collectCpuStats(statFile)
		assert.NoError(t, err)
		assert.NotNil(t, stats)
		expected := map[int64]*CPUStat{
			0: {User: 100, Nice: 10, System: 200, Idle: 300, Iowait: 40, Irq: 50, Softirq: 60, Steal: 70, Guest: 80, GuestNice: 90},
			1: {User: 101, Nice: 11, System: 201, Idle: 301, Iowait: 41, Irq: 51, Softirq: 61, Steal: 71, Guest: 0, GuestNice: 0},
			2: {User: 102, Nice: 12, System: 202, Idle: 302, Iowait: 42, Irq: 52, Softirq: 62, Steal: 0, Guest: 0, GuestNice: 0},
		}
		assert.Equal(t, expected, stats)
	})

	t.Run("missing stat file errors", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		stats, err := collectCpuStats(filepath.Join(root, "missing-stat"))
		assert.Nil(t, stats)
		assert.Error(t, err)
	})

	t.Run("skips malformed lines", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		statFile := filepath.Join(root, "stat")
		content := `cpu0 100 10 200 300 40 50 60 70 80 90
cpu_invalid 1 2 3 4 5 6 7
cpu1 1 2 3
cpu2 1 not_a_number 3 4 5 6 7
cpu3 103 13 203 303 43 53 63
`
		writeSysFile(t, statFile, content)

		stats, err := collectCpuStats(statFile)
		assert.NoError(t, err)
		expected := map[int64]*CPUStat{
			0: {User: 100, Nice: 10, System: 200, Idle: 300, Iowait: 40, Irq: 50, Softirq: 60, Steal: 70, Guest: 80, GuestNice: 90},
			3: {User: 103, Nice: 13, System: 203, Idle: 303, Iowait: 43, Irq: 53, Softirq: 63, Steal: 0, Guest: 0, GuestNice: 0},
		}
		assert.Equal(t, expected, stats)
	})

	t.Run("empty stat file yields empty map", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		statFile := filepath.Join(root, "stat")
		writeSysFile(t, statFile, "")

		stats, err := collectCpuStats(statFile)
		assert.NoError(t, err)
		assert.NotNil(t, stats)
		assert.Empty(t, stats)
	})
}

func Test_GetCPUFlags(t *testing.T) {
	t.Parallel()

	t.Run("extracts flags line", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuinfo := filepath.Join(root, "cpuinfo")
		content := "processor\t: 0\n" +
			"vendor_id\t: GenuineIntel\n" +
			"flags\t\t: fpu vme de pse tsc msr pae mce cx8 apic sep mtrr pge mca cmov\n"
		writeSysFile(t, cpuinfo, content)

		flags, err := getCPUFlagsFromFile(cpuinfo)
		assert.NoError(t, err)
		expected := []string{"fpu", "vme", "de", "pse", "tsc", "msr", "pae", "mce", "cx8", "apic", "sep", "mtrr", "pge", "mca", "cmov"}
		assert.Equal(t, expected, flags)
	})

	t.Run("missing file errors", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		flags, err := getCPUFlagsFromFile(filepath.Join(root, "missing-cpuinfo"))
		assert.Nil(t, flags)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "could not read file")
	})

	t.Run("no flags line errors", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuinfo := filepath.Join(root, "cpuinfo")
		content := "processor\t: 0\n" +
			"vendor_id\t: GenuineIntel\n" +
			"model name\t: Intel(R) Core(TM) i9-9900K CPU @ 3.60GHz\n"
		writeSysFile(t, cpuinfo, content)

		flags, err := getCPUFlagsFromFile(cpuinfo)
		assert.Nil(t, flags)
		assert.Error(t, err)
		assert.Equal(t, "unable to find cpu flag info", err.Error())
	})

	t.Run("empty flags line errors", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuinfo := filepath.Join(root, "cpuinfo")
		content := "processor\t: 0\n" +
			"vendor_id\t: GenuineIntel\n" +
			"flags\t\t: \n"
		writeSysFile(t, cpuinfo, content)

		flags, err := getCPUFlagsFromFile(cpuinfo)
		assert.Nil(t, flags)
		assert.Error(t, err)
		assert.Equal(t, "unable to find cpu flag info", err.Error())
	})

	t.Run("malformed flags line without colon errors", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		cpuinfo := filepath.Join(root, "cpuinfo")
		content := "processor\t: 0\n" +
			"vendor_id\t: GenuineIntel\n" +
			"flags fpu vme de\n"
		writeSysFile(t, cpuinfo, content)

		flags, err := getCPUFlagsFromFile(cpuinfo)
		assert.Nil(t, flags)
		assert.Error(t, err)
		assert.Equal(t, "unable to find cpu flag info", err.Error())
	})
}
