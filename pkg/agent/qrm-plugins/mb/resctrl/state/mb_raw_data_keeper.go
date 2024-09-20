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

package state

import (
	"fmt"
	"path"
	"sync"
	"time"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/consts"
)

type MBRawDataCleaner interface {
	Cleanup(monGroup string)
}

type MBRawDataKeeper interface {
	Set(ccdMonTarget string, value int64, readTime time.Time)
	Get(ccdMonTarget string) (*RawData, error)
	MBRawDataCleaner
}

// RawData keeps the last seen state for one specific ccd mon raw Value
type RawData struct {
	Value    int64
	ReadTime time.Time
}

type mbRawDataKeeper struct {
	rwLock sync.RWMutex
	data   map[string]*RawData
}

func (m *mbRawDataKeeper) Cleanup(monGroup string) {
	m.rwLock.RLock()
	m.rwLock.RUnlock()

	// todo: avoid hard code of 16 CCDs
	for ccd := 0; ccd < 16; ccd++ {
		ccdMon := fmt.Sprintf(consts.TmplCCDMonFolder, ccd)
		monPath := path.Join(monGroup, consts.MonData, ccdMon, consts.MBRawFile)
		delete(m.data, monPath)
	}
}

func (m *mbRawDataKeeper) Set(ccdMonTarget string, value int64, readTime time.Time) {
	m.rwLock.RLock()
	m.rwLock.RUnlock()
	vt, ok := m.data[ccdMonTarget]
	if !ok {
		m.data[ccdMonTarget] = &RawData{
			Value:    value,
			ReadTime: readTime,
		}
		return
	}

	vt.Value = value
	vt.ReadTime = readTime
}

func (m *mbRawDataKeeper) Get(ccdMonTarget string) (*RawData, error) {
	m.rwLock.RLock()
	m.rwLock.RUnlock()
	vt, ok := m.data[ccdMonTarget]
	if !ok {
		return nil, errors.Errorf("raw data not found for %q", ccdMonTarget)
	}

	return vt, nil
}

func NewMBRawDataKeeper() (MBRawDataKeeper, error) {
	return &mbRawDataKeeper{
		data: make(map[string]*RawData),
	}, nil
}
