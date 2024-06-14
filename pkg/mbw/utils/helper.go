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

package utils

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	SYS_CACHE_LINE_BYTE = 64

	SYS_DEVICE_SYSTEM_NODE_PATH = "/sys/devices/system/node/"
	SYS_DEVICE_SYSTEM_CPU_PATH  = "/sys/devices/system/cpu/"
	CPU_FREQUENCY_PATH_AMD      = "cpufreq/cpuinfo_cur_freq"
	CPU_FREQUENCY_PATH_INTEL    = "cpufreq/scaling_cur_freq"

	DEFAULT_CPU_CLOCK   = 0.3846 // calcualted with the frequency of 2.6GHZ
	CPU_CACHE_LEVEL_LLC = 3

	L3_LAT_FACTOR = 16.0
)

func Contains(array []int, item int) bool {
	for _, v := range array {
		if v == item {
			return true
		}
	}

	return false
}

func Delta(bit int, new, old uint64) uint64 {
	var max uint64
	var restart_cnt uint32 = 0
	switch bit {
	case 16:
		max = 0xffff
	case 32:
		max = 0xffffffff
	case 48:
		max = 0xffffffffffff
	case 62:
		max = 0x3fffffffffffffff
	default:
		max = 0xffffffffffffffff
	}

	if old == 0 {
		// ignore the 1st sec mb result after running the monitor
		return 0
	}

	if bit < 64 {
	restart:
		if new >= old {
			return new - old
		} else {
			if restart_cnt < 10 {
				new = max + new //multiple 48bit counter accumulated,such as pkg mem bandwidth
				restart_cnt++
				goto restart
			} else {
				return new - old
			}
		}
	} else if bit == 64 {
		return max - old + new
	}

	return 0
}

func RDTEventToMB(event, interval, scalar uint64) uint64 {
	return BytesToMB(event*scalar) * 1000 / interval
}

// the latency uint is core clock
// 1 core clock is 1 / max_frequency, around 3ns on AMD Genoa
func L3PMCToLatency(count1, count2, interval uint64) float64 {
	if count2 == 0 {
		return 0
	}

	return (L3_LAT_FACTOR * float64(count1) / float64(count2)) * 1000 / float64(interval)
}

func PMUToMB(count, interval uint64) uint64 {
	return BytesToMB(count*SYS_CACHE_LINE_BYTE) * 1000 / interval
}

func BytesToMB(b uint64) uint64 {
	return b / 1024 / 1024
}

func BytesToGB(b uint64) uint64 {
	return b / 1024 / 1024 / 1024
}

func GetCPUFrequency(cpu int, vendor string) (int, error) {
	var path string

	switch vendor {
	case "AMD":
		path = filepath.Join(
			SYS_DEVICE_SYSTEM_CPU_PATH,
			fmt.Sprintf("cpu%d", cpu),
			CPU_FREQUENCY_PATH_AMD)
	case "Intel":
		path = filepath.Join(
			SYS_DEVICE_SYSTEM_CPU_PATH,
			fmt.Sprintf("cpu%d", cpu),
			CPU_FREQUENCY_PATH_INTEL)
	}

	freq, err := general.ReadFileIntoInt(path)
	if err != nil {
		fmt.Printf("failed to get frequency of cpu %d - %v\n", cpu, err)
		return -1, err
	}

	return freq, nil
}

func GetCPUClock(cpu int, family string) float64 {
	freq, err := GetCPUFrequency(cpu, family)
	if err != nil {
		return DEFAULT_CPU_CLOCK
	}

	return 1.0 / (float64(freq) / 1000000.0)
}

// get the CCD topology of AMD CPUs which represents the CCD mapping to CPU cores.
// AMD proccessors with Zen architecture have a separate LLC on each CCX/CCD (1 CCD has 1 CCX on Zen3/4/...),
// thus we can tell a unique CCD by identifying LLCs
// @param numNuma: the amount of Numa nodes on this machine
func GetCCDTopology(numNuma int) (map[int][]int, error) {
	ccdMap := make(map[int][]int)
	ccdIdx := 0

	for nodeID := 0; nodeID < numNuma; nodeID++ {
		path := filepath.Join(SYS_DEVICE_SYSTEM_NODE_PATH, fmt.Sprintf("node%d", nodeID))
		files, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}

		llcMap := make(map[string][]int)
		for _, file := range files {
			filename := file.Name()
			if !strings.HasPrefix(filename, "cpu") {
				continue
			}
			if filename == "cpumap" || filename == "cpulist" {
				// There are two files in the node directory that start with 'cpu'
				// but are not subdirectories ('cpulist' and 'cpumap'). Ignore
				// these files.
				continue
			}
			// Grab the logical processor ID by cutting the integer from the
			// /sys/devices/system/node/nodeX/cpuX filename
			cpuPath := filepath.Join(path, filename)
			lpID, _ := strconv.Atoi(filename[3:])

			// Inspect the caches for each logical processor. There will be a
			// /sys/devices/system/node/nodeX/cpuX/cache directory containing a
			// number of directories beginning with the prefix "index" followed by
			// a number. The number indicates the level of the cache, which
			// indicates the "distance" from the processor. Each of these
			// directories contains information about the size of that level of
			// cache and the processors mapped to it. We only focus on the L3 caches
			// here to retrieve the CCX/CCD domains on this machine, so index3 is the
			// target file we gonna read.
			cachePath := filepath.Join(cpuPath, "cache")
			if _, err = os.Stat(cachePath); errors.Is(err, os.ErrNotExist) {
				continue
			}
			cacheDirFiles, err := os.ReadDir(cachePath)
			if err != nil {
				return nil, err
			}
			for _, cacheDirFile := range cacheDirFiles {
				cacheDirFileName := cacheDirFile.Name()
				if !strings.HasPrefix(cacheDirFileName, "index") {
					continue
				}
				cacheIndex, _ := strconv.Atoi(cacheDirFileName[5:])
				if cacheIndex != CPU_CACHE_LEVEL_LLC {
					continue
				}

				// The cache information is repeated for each node, so here, we
				// just ensure that we only have a one Cache object for each
				// unique combination of level, type and processor map.
				// an example sharedCpuList "0-7,192-199" indicates a list of cpu cores sharing the same LLC
				sharedCpuList := memoryCacheSharedCPUList(nodeID, lpID, cacheIndex)

				if _, exists := llcMap[sharedCpuList]; !exists {
					llcMap[sharedCpuList] = make([]int, 0)
				}
				llcMap[sharedCpuList] = append(llcMap[sharedCpuList], lpID)
			}
		}

		// ensure the LLC's processor set is sorted by logical processor ID
		// otherwise, the CCD mappings might be different time to time
		llcList := sortSharedCPUList(llcMap)
		for _, c := range llcList {
			cpuList := llcMap[c]
			sort.Ints(cpuList)
			ccdMap[ccdIdx] = cpuList
			ccdIdx++
		}
	}

	return ccdMap, nil
}

func sortSharedCPUList(sharedCPUMap map[string][]int) []string {
	keys := make([]string, 0)

	for key := range sharedCPUMap {
		keys = append(keys, key)
	}

	// example keys: ["104-111,296-303", "96-103,288-295"]
	sort.SliceStable(keys, func(i, j int) bool {
		return len(keys[i]) < len(keys[j]) || (len(keys[i]) == len(keys[j]) && keys[i] < keys[j])
	})

	return keys
}

func memoryCacheSharedCPUList(nodeID, lpID, cacheIndex int) string {
	scpuPath := filepath.Join(
		getNodeCPUCacheIndexPath(nodeID, lpID, cacheIndex),
		"shared_cpu_list",
	)
	sharedCpuList, err := os.ReadFile(scpuPath)
	if err != nil {
		return ""
	}
	return string(sharedCpuList[:len(sharedCpuList)-1])
}

func getNodeCPUPath(nodeID int, lpID int) string {
	return filepath.Join(
		SYS_DEVICE_SYSTEM_NODE_PATH,
		fmt.Sprintf("node%d", nodeID),
		fmt.Sprintf("cpu%d", lpID),
	)
}

func getNodeCPUCachePath(nodeID int, lpID int) string {
	return filepath.Join(
		getNodeCPUPath(nodeID, lpID),
		"cache",
	)
}

func getNodeCPUCacheIndexPath(nodeID int, lpID int, cacheIndex int) string {
	return filepath.Join(
		getNodeCPUCachePath(nodeID, lpID),
		fmt.Sprintf("index%d", cacheIndex),
	)
}
