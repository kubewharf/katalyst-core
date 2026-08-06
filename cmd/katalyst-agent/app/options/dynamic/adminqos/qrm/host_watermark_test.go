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

	dynamicqrm "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/adminqos/qrm"
)

func TestHostWatermarkOptionsDefaultsAndFlags(t *testing.T) {
	t.Parallel()

	as := require.New(t)
	options := NewHostWatermarkOptions()
	as.False(options.EnableHostWatermark)
	as.Equal(500, options.VMWatermarkScaleFactor)
	as.Equal(15000, options.VMWatermarkBoostFactor)
	as.Equal(500, options.VMExtFragThreshold)
	as.Equal(uint64(10), options.ReservedKswapdWatermarkGB)

	fss := cliflag.NamedFlagSets{}
	options.AddFlags(&fss)
	fs := fss.FlagSet("memory_resource_plugin")
	as.NotNil(fs.Lookup("enable-setting-host-watermark"))
	as.NotNil(fs.Lookup("qrm-memory-vm-watermark-scale-factor"))
	as.NotNil(fs.Lookup("qrm-memory-vm-watermark-boost-factor"))
	as.NotNil(fs.Lookup("qrm-memory-vm-extfrag-threshold"))
	as.NotNil(fs.Lookup("qrm-memory-kswapd-watermark-reserved-gb"))
	as.NoError(fs.Parse([]string{
		"--enable-setting-host-watermark=true",
		"--qrm-memory-vm-watermark-scale-factor=1234",
		"--qrm-memory-vm-watermark-boost-factor=99",
		"--qrm-memory-vm-extfrag-threshold=500",
		"--qrm-memory-kswapd-watermark-reserved-gb=10",
	}))

	conf := dynamicqrm.NewHostWatermarkConfiguration()
	as.NoError(options.ApplyTo(conf))
	as.True(conf.EnableHostWatermark)
	as.Equal(1234, conf.VMWatermarkScaleFactor)
	as.Equal(99, conf.VMWatermarkBoostFactor)
	as.Equal(500, conf.VMExtFragThreshold)
	as.Equal(uint64(10), conf.ReservedKswapdWatermarkGB)
}
