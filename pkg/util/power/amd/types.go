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

package amd

import (
	"fmt"

	utils "github.com/kubewharf/katalyst-core/pkg/util/lowlevel"
)

const (
	MACHINE_INFO_VENDER_AMD   = "AMD"
	MACHINE_INFO_VENDER_INTEL = "Intel"
	AMD_ZEN4_GENOA_A          = 0x10
	AMD_ZEN2_ROME             = 0x31
	AMD_ZEN3_MILAN            = 0x01
)

type SysInfo struct {
	VenderID      string
	Family        int
	Model         int
	SocketNum     int
	NumaNum       int
	NumaPerSocket int
	PciDevID      uint16
	PowerMaxLimit []uint32 //the max power limit can be set for each socket
	CCDMapping    map[int][]int
}

type CMDInput struct {
	CCDList        string
	SocketList     string
	BandwidthLimit uint64
	PowerLimit     uint32
	LatencyLimit   uint32
	SetLimit       bool
}

type Operation struct {
	MachineInfo SysInfo
	Input       CMDInput
	IOHCs       []*utils.PCIDev
}

func NewSysInfo() (SysInfo, error) {
	sysInfo := SysInfo{
		VenderID:  utils.GetVenderID(),
		Family:    utils.GetCPUFamily(),
		Model:     utils.GetCPUModel(),
		SocketNum: utils.GetSocketNum(),
		NumaNum:   utils.GetNumaNum(),
	}

	if sysInfo.SocketNum == 0 {
		return sysInfo, fmt.Errorf("invalid socket number")
	}

	if sysInfo.SocketNum == 0 {
		return sysInfo, fmt.Errorf("invalid numa per socket number")
	}

	sysInfo.PowerMaxLimit = make([]uint32, sysInfo.SocketNum)
	sysInfo.NumaPerSocket = sysInfo.NumaNum / sysInfo.SocketNum
	ccdMapping, err := utils.GetCCDTopology(sysInfo.NumaNum)
	if err != nil {
		fmt.Printf("failed to discover the CCD mappings on this machine - %v", err)
		return sysInfo, err
	}
	sysInfo.CCDMapping = ccdMapping

	if sysInfo.IsGenoa() {
		sysInfo.PciDevID = AMD_GENOA_IOHC
	} else if sysInfo.IsMilan() || sysInfo.IsRome() {
		sysInfo.PciDevID = AMD_ROME_IOHC
	}

	return sysInfo, nil
}

func NewOperation() (Operation, error) {
	sysInfo, err := NewSysInfo()
	if err != nil {
		return Operation{}, fmt.Errorf("failed to init sysinfo - %v", err)
	}

	op := Operation{
		MachineInfo: sysInfo,
		Input:       CMDInput{},
		IOHCs:       make([]*utils.PCIDev, sysInfo.SocketNum),
	}

	return op, nil
}

func (s SysInfo) IsAMD() bool {
	return s.VenderID == MACHINE_INFO_VENDER_AMD
}

func (s SysInfo) IsGenoa() bool {
	if !s.IsAMD() {
		return false
	}

	return s.Family >= 0x19 && s.Model >= AMD_ZEN4_GENOA_A
}

func (s SysInfo) IsRome() bool {
	if !s.IsAMD() {
		return false
	}

	return s.Family >= 0x17 && s.Model == AMD_ZEN2_ROME
}

func (s SysInfo) IsMilan() bool {
	if !s.IsAMD() {
		return false
	}

	return s.Family == 0x19 && s.Model == AMD_ZEN3_MILAN
}
