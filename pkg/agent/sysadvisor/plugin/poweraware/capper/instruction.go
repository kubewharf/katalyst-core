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

package capper

import (
	"fmt"
	"strconv"

	"github.com/pkg/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
)

type PowerCapOpCode string

const (
	keyOpCode         = "op-code"
	keyOpCurrentValue = "op-current-value"
	keyOpTargetValue  = "op-target-value"

	OpCap     PowerCapOpCode = "4"
	OpReset   PowerCapOpCode = "-1"
	OpUnknown PowerCapOpCode = "-2"
)

var PowerCapReset = &CapInstruction{
	OpCode: OpReset,
}

type CapInstruction struct {
	OpCode         PowerCapOpCode
	OpCurrentValue string
	OpTargetValue  string
}

func (c CapInstruction) ToListAndWatchResponse() *advisorsvc.ListAndWatchResponse {
	return &advisorsvc.ListAndWatchResponse{
		PodEntries: nil,
		ExtraEntries: []*advisorsvc.CalculationInfo{{
			CgroupPath: "",
			CalculationResult: &advisorsvc.CalculationResult{
				Values: map[string]string{
					keyOpCode:         string(c.OpCode),
					keyOpCurrentValue: c.OpCurrentValue,
					keyOpTargetValue:  c.OpTargetValue,
				},
			},
		}},
	}
}

func (c CapInstruction) ToCapRequest() (opCode PowerCapOpCode, targetValue, currentValue int) {
	opCode = c.OpCode
	if OpReset == opCode {
		return OpReset, 0, 0
	}

	var err error
	if targetValue, err = strconv.Atoi(c.OpTargetValue); err != nil {
		return OpUnknown, 0, 0
	}

	if currentValue, err = strconv.Atoi(c.OpCurrentValue); err != nil {
		return OpUnknown, 0, 0
	}

	return opCode, targetValue, currentValue
}

func getCappingInstructionFromCalcInfo(info *advisorsvc.CalculationInfo) (*CapInstruction, error) {
	if info == nil {
		return nil, errors.New("invalid data of nil CalculationInfo")
	}

	calcRes := info.CalculationResult
	if calcRes == nil {
		return nil, errors.New("invalid data of nil CalculationResult")
	}

	values := calcRes.GetValues()
	if len(values) == 0 {
		return nil, errors.New("invalid data of empty Values map")
	}

	opCode, ok := values[keyOpCode]
	if !ok {
		return nil, errors.New("op-code not found")
	}

	opCurrValue := values[keyOpCurrentValue]
	opTargetValue := values[keyOpTargetValue]

	return &CapInstruction{
		OpCode:         PowerCapOpCode(opCode),
		OpCurrentValue: opCurrValue,
		OpTargetValue:  opTargetValue,
	}, nil
}

func GetCappingInstructions(response *advisorsvc.ListAndWatchResponse) ([]*CapInstruction, error) {
	if len(response.ExtraEntries) == 0 {
		return nil, errors.New("no valid data of no capping instruction")
	}

	count := len(response.ExtraEntries)
	cis := make([]*CapInstruction, count)
	for i, calcInfo := range response.ExtraEntries {
		ci, err := getCappingInstructionFromCalcInfo(calcInfo)
		if err != nil {
			return nil, err
		}

		cis[i] = ci
	}

	return cis, nil
}

func NewCapInstruction(targetWatts, currWatt int) (*CapInstruction, error) {
	if targetWatts >= currWatt {
		return nil, errors.New("invalid power cap request")
	}

	return &CapInstruction{
		OpCode:         OpCap,
		OpCurrentValue: fmt.Sprintf("%d", currWatt),
		OpTargetValue:  fmt.Sprintf("%d", targetWatts),
	}, nil
}
