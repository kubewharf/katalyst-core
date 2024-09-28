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

package resctrl

import (
	"fmt"
	"path"
	"time"

	"github.com/spf13/afero"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/file"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type CCDMBCalculator interface {
	CalcMB(monGroup string, ccd int) int
}

type mbCalculator struct {
	// keeping the last seen states of all processed ccd mon raw data
	rawDataKeeper state.MBRawDataKeeper
}

func (c *mbCalculator) CalcMB(monGroup string, ccd int) int {
	return calcMB(afero.NewOsFs(), monGroup, ccd, time.Now(), c.rawDataKeeper)
}

func NewCCDMBCalculator(rawDataKeeper state.MBRawDataKeeper) (CCDMBCalculator, error) {
	return &mbCalculator{
		rawDataKeeper: rawDataKeeper,
	}, nil
}

func calcMB(fs afero.Fs, monGroup string, ccd int, tsCurr time.Time, dataKeeper state.MBRawDataKeeper) int {
	ccdMon := fmt.Sprintf(consts.TmplCCDMonFolder, ccd)
	monPath := path.Join(monGroup, consts.MonData, ccdMon, consts.MBRawFile)

	valueCurr := file.ReadValueFromFile(fs, monPath)
	general.InfofV(6, "mbm: resctrl: read value from file %s: %d", monPath, valueCurr)

	mb := consts.UninitializedMB
	if prev, err := dataKeeper.Get(monPath); err == nil {
		mb = calcAverageInMBps(valueCurr, tsCurr, prev.Value, prev.ReadTime)
	}

	// always refresh the last seen raw record needed for future calc
	dataKeeper.Set(monPath, valueCurr, tsCurr)

	return mb
}

func calcAverageInMBps(currV int64, nowTime time.Time, lastV int64, lastTime time.Time) int {
	if currV == consts.UninitializedMB || lastV == consts.UninitializedMB || currV < lastV {
		return consts.UninitializedMB
	}

	elapsed := nowTime.Sub(lastTime)
	mbInMB := (currV - lastV) / elapsed.Microseconds()
	return int(mbInMB)
}
