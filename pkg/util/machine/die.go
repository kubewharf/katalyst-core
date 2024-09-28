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
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
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

	DieSize   int // how many cpu on a die(CCD)
	CPUs      int
	CPUsInDie map[int][]int // mapping from CCD to cpus
}

func (d DieTopology) String() string {
	return fmt.Sprintf("fake numa enabled: %v\ntotal packages: %d\npackage-numa nodes: %v\ntotal numa nodes: %d, dies %d, cpus %d\nnuma-die: %v\ndie-cpu: %v\n",
		d.FakeNUMAEnabled,
		d.Packages,
		d.NUMAsInPackage,
		d.NUMAs,
		d.Dies,
		d.CPUs,
		d.DiesInNuma,
		d.CPUsInDie)
}

func NewDieTopology(numaDistanceMap map[int][]NumaDistanceInfo) (*DieTopology, error) {
	topo := &DieTopology{}

	// in mbm-poc phase, discover other die topology info by looking at /sys/devices/system/cpu/ tree
	// the critical for mbm-pod is numa node -> die -> cpu list
	var err error
	fs := afero.NewOsFs()
	if topo.FakeNUMAEnabled, err = FakeNUMAEnabled(fs); err != nil {
		general.Warningf("mbm: check for fake numa setting: %v", err)
	}

	topo.NUMAsInPackage = extractPackageNumaNodes(numaDistanceMap, topo.FakeNUMAEnabled)

	cpus, err := getCPUs(fs)
	if err != nil {
		return nil, err
	}
	topo.CPUs = len(cpus)
	topo.DiesInNuma = getDiesInNuma(cpus)
	topo.NUMAs = len(topo.DiesInNuma)
	topo.CPUsInDie = getCPUsInDie(cpus)
	topo.Dies = len(topo.CPUsInDie)
	topo.Packages = len(topo.NUMAsInPackage)

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

// errSkipAll is not really an error, that the entry has been found and hence ignore all remainders
var errSkipAll = errors.New("target found and no need to search more")

func getCPU(fs afero.Fs, id int) (*cpuDev, error) {
	cpuPath := path.Join(rootPath, fmt.Sprintf("cpu%d", id))

	// numa node is as of "nodeX/" folder
	numaNode := -1
	if err := afero.Walk(fs, cpuPath, func(path string, info os.FileInfo, err error) error {
		baseName := strings.TrimPrefix(path, cpuPath)
		if len(baseName) == 0 {
			return nil
		}

		if !strings.HasPrefix(baseName, "/node") {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		node := strings.TrimPrefix(baseName, "/node")
		general.InfofV(6, "mbm: locating cpu %d numa node %q", id, node)
		if numaNode, err = parseInt(node); err != nil {
			return err
		}
		return errSkipAll
	}); err != nil && !errors.Is(err, errSkipAll) {
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

func getOnehotMap(numaDistanceMap map[int][]NumaDistanceInfo) map[int][]int {
	onehots := make(map[int][]int)
	for node, _ := range numaDistanceMap {
		onehots[node] = []int{node}
	}
	return onehots
}

func extractVirtualNumaClusters(numaDistanceMap map[int][]NumaDistanceInfo) map[int][]int {
	max := 0
	for _, infos := range numaDistanceMap {
		for _, info := range infos {
			if max < info.Distance {
				max = info.Distance
			}
		}
	}

	bar := max
	for n, infos := range numaDistanceMap {
		for _, info := range infos {
			if info.NumaID == n {
				continue
			}
			if bar > info.Distance {
				bar = info.Distance
			}
		}
	}

	packages := make(map[int]sets.Int)
	for n, infos := range numaDistanceMap {
		pack := locatePackage(n, packages)
		if pack == nil {
			id := len(packages)
			packages[id] = sets.Int{n: sets.Empty{}}
			pack = packages[id]
		}
		for _, info := range infos {
			if info.Distance == bar {
				pack.Insert(info.NumaID)
			}
		}
	}

	packageNodes := make(map[int][]int)
	for p, nodes := range packages {
		list := nodes.List()
		sort.Ints(list)
		packageNodes[p] = list
	}

	return packageNodes
}

// extractPackageNumaNodes guesses out the package-numa nodes mappings
// based on distances between numa nodes
// distance to self is for sure the minimum one;
// distance the maximum for sure is out of the package;
// distance the second smallest and not the maximum is likely of the package (if it is virtual numa, almost certain it is the case)
func extractPackageNumaNodes(numaDistanceMap map[int][]NumaDistanceInfo, isVirtualNuma bool) map[int][]int {
	if !isVirtualNuma {
		// all numa nodes are real
		return getOnehotMap(numaDistanceMap)
	}
	return extractVirtualNumaClusters(numaDistanceMap)
}

func locatePackage(node int, packages map[int]sets.Int) sets.Int {
	for _, nodes := range packages {
		if nodes.Has(node) {
			return nodes
		}
	}
	return nil
}
