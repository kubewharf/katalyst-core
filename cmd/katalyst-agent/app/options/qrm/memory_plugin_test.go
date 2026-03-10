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

package qrm

import (
	"testing"

	"github.com/stretchr/testify/require"
	cliflag "k8s.io/component-base/cli/flag"

	qrmconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm"
)

func TestNewMemoryOptions_Defaults_HostWatermark(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	o := NewMemoryOptions()

	as.False(o.EnableSettingHostWatermark)
	as.Equal(0, o.SetVMWatermarkScaleFactor)
	as.Equal(uint64(0), o.ReservedKswapdWatermarkGB)
}

func TestMemoryOptions_AddFlags_ParseHostWatermark(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	o := NewMemoryOptions()

	fss := cliflag.NamedFlagSets{}
	o.AddFlags(&fss)
	fs := fss.FlagSet("memory_resource_plugin")

	as.NotNil(fs.Lookup("enable-setting-host-watermark"))
	as.NotNil(fs.Lookup("qrm-memory-vm-watermark-scale-factor"))
	as.NotNil(fs.Lookup("qrm-memory-kswapd-watermark-reserved-gb"))

	as.NoError(fs.Parse([]string{
		"--enable-setting-host-watermark=true",
		"--qrm-memory-vm-watermark-scale-factor=1234",
		"--qrm-memory-kswapd-watermark-reserved-gb=10",
	}))

	as.True(o.EnableSettingHostWatermark)
	as.Equal(1234, o.SetVMWatermarkScaleFactor)
	as.Equal(uint64(10), o.ReservedKswapdWatermarkGB)
}

func TestMemoryOptions_ApplyTo_CopiesHostWatermark(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	o := NewMemoryOptions()
	o.EnableSettingHostWatermark = true
	o.SetVMWatermarkScaleFactor = 333
	o.ReservedKswapdWatermarkGB = 42

	conf := qrmconfig.NewMemoryQRMPluginConfig()
	as.NoError(o.ApplyTo(conf))

	as.True(conf.EnableSettingHostWatermark)
	as.Equal(333, conf.SetVMWatermarkScaleFactor)
	as.Equal(uint64(42), conf.ReservedKswapdWatermarkGB)
}
