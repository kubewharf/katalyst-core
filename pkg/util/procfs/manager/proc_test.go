//go:build linux
// +build linux

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

package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetCPUInfo(t *testing.T) {
	ci, err := GetCPUInfo()
	t.Logf("GetCPUInfo:%+v", ci)
	assert.NoError(t, err)
}

func TestGetProcStat(t *testing.T) {
	stat, err := GetProcStat()
	t.Logf("GetProcStat:%+v", stat)
	assert.NoError(t, err)
}

func TestGetPidComm(t *testing.T) {
	comm, err := GetPidComm(1)
	t.Logf("GetPidComm:%+v", comm)
	assert.NoError(t, err)
}

func TestGetPidCmdline(t *testing.T) {
	cl, err := GetPidCmdline(1)
	t.Logf("GetPidCmdline:%+v", cl)
	assert.NoError(t, err)
}

func TestGetPidCgroups(t *testing.T) {
	cgroups, err := GetPidCgroups(1)
	t.Logf("GetPidCgroups:%+v", cgroups)
	assert.NoError(t, err)
}

func TestGetMounts(t *testing.T) {
	mounts, err := GetMounts()
	t.Logf("GetMounts:%+v", mounts)
	assert.NoError(t, err)
}

func TestGetProcMounts(t *testing.T) {
	mounts, err := GetProcMounts(1)
	t.Logf("GetProcMounts:%+v", mounts)
	assert.NoError(t, err)
}

func TestGetIPVSStats(t *testing.T) {
	stats, err := GetIPVSStats()
	t.Logf("GetIPVSStats:%+v", stats)
	assert.NoError(t, err)
}

func TestGetNetDev(t *testing.T) {
	dev, err := GetNetDev()
	t.Logf("GetNetDev:%+v", dev)
	assert.NoError(t, err)
}

func TestGetNetStat(t *testing.T) {
	stats, err := GetNetStat()
	t.Logf("GetNetStat:%+v", stats)
	assert.NoError(t, err)
}

func TestGetNetTCP(t *testing.T) {
	stats, err := GetNetTCP()
	t.Logf("GetNetTCP:%+v", stats)
	assert.NoError(t, err)
}

func TestGetNetTCP6(t *testing.T) {
	stats, err := GetNetTCP6()
	t.Logf("GetNetTCP6:%+v", stats)
	assert.NoError(t, err)
}

func TestGetNetUDP(t *testing.T) {
	stats, err := GetNetUDP()
	t.Logf("GetNetUDP:%+v", stats)
	assert.NoError(t, err)
}

func TestGetNetUDP6(t *testing.T) {
	stats, err := GetNetUDP6()
	t.Logf("GetNetUDP6:%+v", stats)
	assert.NoError(t, err)
}

func TestGetSoftirqs(t *testing.T) {
	irqs, err := GetSoftirqs()
	t.Logf("GetSoftirqs:%+v", irqs)
	assert.NoError(t, err)
}

func TestGetProcInterrupts(t *testing.T) {
	interrupts, err := GetProcInterrupts()
	t.Logf("GetProcInterrupts:%+v", interrupts)
	assert.NoError(t, err)
}

func TestGetPSIStatsForResource(t *testing.T) {
	tcases := []struct {
		name    string
		reource string
		wantErr bool
	}{
		{
			name:    "get psi stats for cpu resource",
			reource: "cpu",
			wantErr: false,
		},
		{
			name:    "get psi stats for memory resource",
			reource: "memory",
			wantErr: false,
		},
		{
			name:    "get psi stats for io resource",
			reource: "io",
			wantErr: false,
		},
		{
			name:    "get psi stats for test resource",
			reource: "test",
			wantErr: true,
		},
	}

	for _, tc := range tcases {
		t.Run(tc.name, func(t *testing.T) {
			stats, err := GetPSIStatsForResource(tc.reource)
			if tc.wantErr {
				assert.Error(t, err)
			}
			t.Logf("GetPSIStatsForResource:%+v", stats)
		})
	}
}

func TestGetSchedStat(t *testing.T) {
	stats, err := GetSchedStat()
	t.Logf("GetSchedStat:%+v", stats)
	assert.NoError(t, err)
}
