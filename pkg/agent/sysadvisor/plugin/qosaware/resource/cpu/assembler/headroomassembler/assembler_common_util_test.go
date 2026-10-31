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

package headroomassembler

import (
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResolveOverlapReclaim(t *testing.T) {
	t.Parallel()

	ckDir, err := ioutil.TempDir("", "checkpoint-TestResolveOverlapReclaim")
	require.NoError(t, err)
	defer os.RemoveAll(ckDir)

	sfDir, err := ioutil.TempDir("", "statefile-TestResolveOverlapReclaim")
	require.NoError(t, err)
	defer os.RemoveAll(sfDir)

	conf := generateTestConfiguration(t, ckDir, sfDir)
	ha := &HeadroomAssemblerCommon{conf: conf}
	dynamicConf := conf.GetDynamicConfiguration()

	dedicatedNUMAs := map[int]bool{0: true}

	// dedicated-bound NUMA => controlled by DisableDedicatedCoresOverlapReclaimedCores.
	conf.GetDynamicConfiguration().AllowSharedCoresOverlapReclaimedCores = true
	conf.GetDynamicConfiguration().DisableDedicatedCoresOverlapReclaimedCores = false
	require.True(t, ha.resolveOverlapReclaim(dynamicConf, 0, dedicatedNUMAs))
	conf.GetDynamicConfiguration().DisableDedicatedCoresOverlapReclaimedCores = true
	require.False(t, ha.resolveOverlapReclaim(dynamicConf, 0, dedicatedNUMAs))
	conf.GetDynamicConfiguration().AllowSharedCoresOverlapReclaimedCores = false
	conf.GetDynamicConfiguration().DisableDedicatedCoresOverlapReclaimedCores = false
	require.True(t, ha.resolveOverlapReclaim(dynamicConf, 0, dedicatedNUMAs))
	conf.GetDynamicConfiguration().DisableDedicatedCoresOverlapReclaimedCores = true
	require.False(t, ha.resolveOverlapReclaim(dynamicConf, 0, dedicatedNUMAs))

	// share/global NUMA => equals AllowSharedCoresOverlapReclaimedCores.
	conf.GetDynamicConfiguration().DisableDedicatedCoresOverlapReclaimedCores = true
	conf.GetDynamicConfiguration().AllowSharedCoresOverlapReclaimedCores = true
	require.True(t, ha.resolveOverlapReclaim(dynamicConf, 1, dedicatedNUMAs))
	conf.GetDynamicConfiguration().AllowSharedCoresOverlapReclaimedCores = false
	require.False(t, ha.resolveOverlapReclaim(dynamicConf, 1, dedicatedNUMAs))
}
