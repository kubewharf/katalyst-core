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

package pod

import (
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func Test_getDefaultAbsCgroupRootPaths(t *testing.T) {
	name := "test for cgroupfs"
	want := []string{
		"/sys/fs/cgroup/cpu/kubepods",
		"/sys/fs/cgroup/cpu/kubepods/besteffort",
		"/sys/fs/cgroup/cpu/kubepods/burstable",
	}

	t.Run(name, func(t *testing.T) {
		if got := common.GetKubernetesCgroupRootPathWithSubSys("cpu"); !reflect.DeepEqual(got, want) {
			t.Errorf("getAbsCgroupRootPaths() \n got = %v, \n want = %v\n", got, want)
		}
	})
}

func Test_getAdditionAbsCgroupRootPaths(t *testing.T) {
	common.InitKubernetesCGroupPath(common.CgroupTypeSystemd, []string{"/kubepods/test.slice"})

	name := "test for cgroupfs"
	want := []string{
		"/sys/fs/cgroup/cpu/kubepods.slice",
		"/sys/fs/cgroup/cpu/kubepods.slice/kubepods-besteffort.slice",
		"/sys/fs/cgroup/cpu/kubepods.slice/kubepods-burstable.slice",
		"/sys/fs/cgroup/cpu/kubepods/test.slice",
	}

	t.Run(name, func(t *testing.T) {
		if got := common.GetKubernetesCgroupRootPathWithSubSys("cpu"); !reflect.DeepEqual(got, want) {
			t.Errorf("getAbsCgroupRootPaths() \n got = %v, \n want = %v\n", got, want)
		}
	})
}
