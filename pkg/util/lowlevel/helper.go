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

package lowlevel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	. "github.com/klauspost/cpuid/v2"
)

const (
	SYS_CACHE_LINE_BYTE = 64

	SYS_DEVICE_SYSTEM_NODE_PATH = "/sys/devices/system/node/"
	SYS_DEVICE_SYSTEM_CPU_PATH  = "/sys/devices/system/cpu/"
	CPU_FREQUENCY_PATH_AMD      = "cpufreq/cpuinfo_cur_freq"

	DEFAULT_CPU_CLOCK = 0.3846 // calcualted with the frequency of 2.6GHZ

	CPU_CACHE_LEVEL_LLC       = 3
	MACHINE_INFO_VENDER_AMD   = "AMD"
	MACHINE_INFO_VENDER_INTEL = "Intel"
	AMD_ZEN4_GENOA_A          = 0x10
	AMD_ZEN2_ROME             = 0x31
	AMD_ZEN3_MILAN            = 0x01
)

func GetVenderID() string {
	return CPU.VendorID.String()
}

func GetCPUFamily() int {
	return CPU.Family
}

func GetCPUModel() int {
	return CPU.Model
}

func GetSocketNum() int {
	// the result of runtime.NumCPU() could be smaller than the total amount of CPU cores
	if runtime.NumCPU()%CPU.LogicalCores == 0 {
		return runtime.NumCPU() / CPU.LogicalCores
	} else {
		return runtime.NumCPU()/CPU.LogicalCores + 1
	}
}

func GetNumaNum() int {
	numa := 0

	files, err := os.ReadDir(SYS_DEVICE_SYSTEM_NODE_PATH)
	if err != nil {
		fmt.Printf("failed to read path %s for listing numa nodes\n", SYS_DEVICE_SYSTEM_NODE_PATH)
		return 0
	}

	for _, file := range files {
		filename := file.Name()
		if !strings.HasPrefix(filename, "node") {
			continue
		}

		numa++
	}

	return numa
}

// ParseRange takes a string representing a range and returns a slice of integers
// ParseRange takes a string representing a mixed list of ranges and individual integers
// and returns a slice of integers
func ParseRange(r string) ([]int, error) {
	var result []int
	elements := strings.Split(r, ",")
	for _, elem := range elements {
		if strings.Contains(elem, "-") {
			// This is a range
			bounds := strings.Split(elem, "-")
			if len(bounds) != 2 {
				return nil, fmt.Errorf("invalid range format: %s", elem)
			}
			start, err1 := strconv.Atoi(bounds[0])
			end, err2 := strconv.Atoi(bounds[1])
			if err1 != nil || err2 != nil {
				return nil, fmt.Errorf("invalid number in range: %s", elem)
			}
			for i := start; i <= end; i++ {
				result = append(result, i)
			}
		} else {
			// This is a single integer
			num, err := strconv.Atoi(elem)
			if err != nil {
				return nil, fmt.Errorf("invalid number: %s", elem)
			}
			result = append(result, num)
		}
	}
	return result, nil
}

func DisplaySpecifiedCCDMapping(ccdMapping map[int][]int, ccds []int) {
	for _, ccd := range ccds {
		fmt.Printf("CCD %d: %v\n", ccd, ccdMapping[ccd])
	}
}

func DisplayCCDMapping(ccdMapping map[int][]int, numNuma int) {
	fmt.Printf("%d numa nodes found \n", numNuma)

	// get all ccds and sort them
	ccds := make([]int, 0)
	for ccd := range ccdMapping {
		ccds = append(ccds, ccd)
	}

	sort.Ints(ccds)

	// print the ccd mappings in asending order
	for _, ccd := range ccds {
		fmt.Printf("CCD %d: %v\n", ccd, ccdMapping[ccd])
	}
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

// ReadFileIntoInt read contents from the given file, and parse them into integer
func ReadFileIntoInt(filepath string) (int, error) {
	body, err := os.ReadFile(filepath)
	if err != nil {
		return 0, fmt.Errorf("read file failed with error: %v", err)
	}

	i, err := strconv.Atoi(strings.TrimSpace(string(body)))
	if err != nil {
		return 0, fmt.Errorf("convert file content to int failed with error: %v", err)
	}

	return i, nil
}

func GetCPUFrequency(cpu int) (int, error) {
	path := filepath.Join(
		SYS_DEVICE_SYSTEM_CPU_PATH,
		fmt.Sprintf("cpu%d", cpu),
		CPU_FREQUENCY_PATH_AMD,
	)

	freq, err := ReadFileIntoInt(path)
	if err != nil {
		fmt.Printf("failed to get frequency of cpu %d - %v\n", cpu, err)
		return -1, err
	}

	return freq, nil
}

func GetCPUClock(cpu int) float64 {
	freq, err := GetCPUFrequency(cpu)
	if err != nil {
		return DEFAULT_CPU_CLOCK
	}

	return 1.0 / (float64(freq) / 1000000.0)
}
