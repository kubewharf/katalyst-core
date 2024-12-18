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
	"errors"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/readmb/rmbtype"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/mb/resctrl/consts"
)

var ErrUninitialized = errors.New("uninitialized mb value")

type CCDMBReader interface {
	ReadMB(MonGroup string, ccd int) (rmbtype.MBStat, error)
}

func NewCCDMBReader(ccdMBCalc CCDMBCalculator) (CCDMBReader, error) {
	return &ccdMBReader{
		ccdMBCalc: ccdMBCalc,
	}, nil
}

type ccdMBReader struct {
	ccdMBCalc CCDMBCalculator
}

func (c ccdMBReader) ReadMB(MonGroup string, ccd int) (rmbtype.MBStat, error) {
	val := c.ccdMBCalc.CalcMB(MonGroup, ccd)
	if val.Total == consts.UninitializedMB {
		return val, ErrUninitialized
	}

	return val, nil
}
