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

	"github.com/pkg/errors"
	"github.com/spf13/afero"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const cpuFsRoot = "/sys/devices/system/cpu/"

var errFound = errors.New("target found and skip all")

type DieTopology struct {
	numaToDie map[int]sets.Int
	dieToNuma map[int]int
}

type cpuInfo struct {
	cpuID     int
	nodeID    int
	l3CacheID int
}

type cpuInfoGetter interface {
	Get(cpuID int) (*cpuInfo, error)
}

func GetDieTopology(numCPU int) (*DieTopology, error) {
	return getDieTopology(newCPUInfoGetter(), numCPU)
}

func getDieTopology(infoGetter cpuInfoGetter, numCPU int) (*DieTopology, error) {
	result := &DieTopology{
		numaToDie: make(map[int]sets.Int),
		dieToNuma: make(map[int]int),
	}

	for id := 0; id < numCPU; id++ {
		info, err := infoGetter.Get(id)
		if err != nil {
			return nil, errors.Wrap(err, "failed to get cpu die-numa topology")
		}

		result.processCPU(id, info.l3CacheID, info.nodeID)
	}

	return result, nil
}

func (d *DieTopology) processCPU(cpuID, dieID, numaID int) {
	d.dieToNuma[dieID] = numaID

	if _, ok := d.numaToDie[numaID]; !ok {
		d.numaToDie[numaID] = make(sets.Int)
	}
	d.numaToDie[numaID].Insert(dieID)
}

type procFsCPUInfoGetter struct {
	fs afero.Fs
}

func (p *procFsCPUInfoGetter) Get(cpuID int) (*cpuInfo, error) {
	l3CacheID, err := p.getL3CacheID(cpuID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cpu info")
	}

	nodeID, err := p.getNumaID(cpuID)
	if err != nil {
		return nil, errors.Wrap(err, "failed to get cpu info")
	}

	return &cpuInfo{
		cpuID:     cpuID,
		nodeID:    nodeID,
		l3CacheID: l3CacheID,
	}, nil
}

func (p *procFsCPUInfoGetter) getNumaID(cpuID int) (int, error) {
	// numa node is as of "nodeX/" folder
	cpuPath := path.Join(cpuFsRoot, fmt.Sprintf("cpu%d", cpuID))

	numaNode := -1
	if err := afero.Walk(p.fs, cpuPath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

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

		nodeStr := strings.TrimPrefix(baseName, "/node")
		var errCurrent error
		if numaNode, errCurrent = parseInt(nodeStr); errCurrent != nil {
			return errors.Wrapf(errCurrent, "failed to locate numa node for cpu %d", cpuID)
		}

		return errFound
	}); err != nil && !errors.Is(err, errFound) {
		return -1, err
	}

	return numaNode, nil
}

func (p *procFsCPUInfoGetter) getL3CacheID(cpuID int) (int, error) {
	// CCD is as of cache/index3/id
	ccdPath := path.Join(cpuFsRoot, fmt.Sprintf("cpu%d/cache/index3/id", cpuID))
	var f afero.File
	f, err := p.fs.Open(ccdPath)
	if err != nil {
		return -1, err
	}
	defer func(f afero.File) {
		err := f.Close()
		if err != nil {
			general.Warningf("[mbm] failed to close file %s: %v", ccdPath, err)
		}
	}(f)

	var idContent []byte
	if idContent, err = io.ReadAll(f); err != nil {
		return -1, errors.Wrapf(err, "failed to read file %s", ccdPath)
	}

	var ccdID int
	if ccdID, err = parseInt(string(idContent)); err != nil {
		return -1, errors.Wrapf(err, "unexpected content of file %s", ccdPath)
	}

	return ccdID, nil
}

func parseInt(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}

func newCPUInfoGetter() cpuInfoGetter {
	return &procFsCPUInfoGetter{}
}
