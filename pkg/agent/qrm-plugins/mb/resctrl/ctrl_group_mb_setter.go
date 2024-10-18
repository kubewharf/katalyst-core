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
	"strings"
)

const mbUnitAMD = 1_000 / 8 // AMD specific value of mb unit is 1/8th GBps

type CtrlGroupMBSetter interface {
	SetMB(ctrlGroup string, ccdMB map[int]int) error
}

type ctrlGroupMBSetter struct {
	schemataUpdater SchemataUpdater
}

func toSchmataInst(ccdMB map[int]int) string {
	//the resultant string looks like  "MB:2=32;3=32;"
	var sb strings.Builder
	sb.WriteString("MB:")
	for ccd, mb := range ccdMB {
		v := (mb + mbUnitAMD - 1) / mbUnitAMD
		sb.WriteString(fmt.Sprintf("%d=%d;", ccd, v))
	}
	// LF is critical for schemata update
	sb.WriteString("\n")
	return sb.String()
}

func (c ctrlGroupMBSetter) SetMB(ctrlGroup string, ccdMB map[int]int) error {
	return c.schemataUpdater.UpdateSchemata(ctrlGroup, toSchmataInst(ccdMB))
}

func NewCtrlGroupSetter(ccdMBSetter SchemataUpdater) (CtrlGroupMBSetter, error) {
	return &ctrlGroupMBSetter{
		schemataUpdater: ccdMBSetter,
	}, nil
}
