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
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/util/sets"
)

// DieTopology keeps the relationship of dies(CCDs), numa, package, and socket
type DieTopology struct {
	// "fake" numa is OS made sub-numa, access across some fake numa nodes could be as efficient as inside,
	// is they are really in one "real" numa domain (package)
	FakeNUMAEnabled bool // if the fake NUMA is configured on this server

	// number of CPU sockets on whole machine
	CPUSockets int

	// package is concept of "real" numa, within which the mem access is equally efficient. and outside much more inefficient
	// e.g. NPS2 makes 2 PACKAGES per CPU socket, NPS1 1 PACKAGES per socket,
	// whileas the (fake) numa nodes could be 4 or 8 per socket
	PackagesPerSocket int // number of pakcage ("real" numa") on one CPU socket, e.g. NPS1 it is 1
	Packages          int // number of "physical" NUMA domains on whole machine. it is CPUSockets * PackagesPerSocket
	PackagesInSocket  map[int][]int

	NUMAs          int           // os made sub numa node number
	NUMAsInPackage map[int][]int // mapping from Package to Numa nodes

	Dies       int              // number of die(CCD)s on whole machine
	DiesInNuma map[int]sets.Int // mapping from Numa to CCDs

	DieSize   int           // how many cpu on a die(CCD)
	CPUsInDie map[int][]int // mapping from CCD to cpus
}

func NewDieTopology(siblingMap map[int]sets.Int) (*DieTopology, error) {
	topo := &DieTopology{}
	topo.NUMAsInPackage = GetNUMAsInPackage(siblingMap)

	// in mbm-poc phase, discover other die topology info by looking at /sys/devices/system/cpu/ tree
	// the critical for mbm-pod is numa node -> die -> cpu list
	fs := afero.NewOsFs()
	cpus, err := getCPUs(fs)
	if err != nil {
		return nil, err
	}
	topo.DiesInNuma = getDiesInNuma(cpus)
	topo.NUMAs = len(topo.DiesInNuma)
	topo.CPUsInDie = getCPUsInDie(cpus)
	topo.Dies = len(topo.CPUsInDie)

	return topo, nil
}

func getDiesInNuma(cpus []*cpuDev) map[int]sets.Int {
	result := make(map[int]sets.Int)
	for _, cpu := range cpus {
		if _, ok := result[cpu.numaNode]; !ok {
			result[cpu.numaNode] = make(sets.Int)
		}
		result[cpu.numaNode].Insert(cpu.ccd)
	}
	return result
}

func getCPUsInDie(cpus []*cpuDev) map[int][]int {
	result := make(map[int][]int)
	for _, cpu := range cpus {
		if _, ok := result[cpu.ccd]; !ok {
			result[cpu.ccd] = make([]int, 0)
		}
		result[cpu.ccd] = append(result[cpu.ccd], cpu.id)
	}
	return result
}

type cpuDev struct {
	id       int
	numaNode int
	ccd      int
}

const rootPath = "/sys/devices/system/cpu/"

func getCPU(fs afero.Fs, id int) (*cpuDev, error) {
	cpuPath := path.Join(rootPath, fmt.Sprintf("cpu%d", id))

	// numa node is as of "nodeX/" folder
	numaNode := -1
	if err := afero.Walk(fs, cpuPath, func(path string, info os.FileInfo, err error) error {
		if !info.IsDir() {
			return nil
		}

		baseName := strings.TrimPrefix(path, cpuPath)
		if len(baseName) == 0 {
			return nil
		}

		if !strings.HasPrefix(baseName, "/node") {
			return filepath.SkipDir
		}

		node := strings.TrimPrefix(baseName, "/node")
		if numaNode, err = parseInt(node); err != nil {
			return err
		}
		return filepath.SkipAll
	}); err != nil && err != filepath.SkipAll {
		return nil, err
	}

	// CCD is as of cache/index3/id
	ccd := -1
	var idContent []byte
	ccdPath := path.Join(cpuPath, "cache/index3/id")
	var f afero.File
	f, err := fs.Open(ccdPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if idContent, err = io.ReadAll(f); err != nil {
		return nil, err
	}
	if ccd, err = parseInt(string(idContent)); err != nil {
		return nil, err
	}

	return &cpuDev{
		id:       id,
		numaNode: numaNode,
		ccd:      ccd,
	}, nil
}

func getCPUTotalNumber(fs afero.Fs) int {
	// get the cpu number by looking for "/sys/devices/system/cpu/cpu*"
	id := 0
	for {
		cpuPath := path.Join(rootPath, fmt.Sprintf("cpu%d", id))
		if _, err := fs.Stat(cpuPath); err != nil {
			break
		}
		id++
	}

	return id
}

func getCPUs(fs afero.Fs) ([]*cpuDev, error) {
	numCPU := getCPUTotalNumber(fs)
	cpus := make([]*cpuDev, numCPU)

	var err error
	for i := 0; i < numCPU; i++ {
		if cpus[i], err = getCPU(fs, i); err != nil {
			return nil, err
		}
	}

	return cpus, nil
}

func parseInt(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
