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

var (
	testKataJsonInfo    = `{"sandboxID": "12345678", "pid": 1234, "runtimeType": "io.containerd.kata.v2"}`
	testInvalidJsonInfo = `{"pid: 2345"}` // no sandbox id field
	testNonKataJsonInfo = `{"sandboxID": "234567890", "pid": "2345", "runtimeType": "docker"}`
)

func Test_getCgroupRootPaths(t *testing.T) {
	t.Parallel()

	want := []string{
		"/sys/fs/cgroup/cpu/kubepods",
		"/sys/fs/cgroup/cpu/kubepods/besteffort",
		"/sys/fs/cgroup/cpu/kubepods/burstable",
	}

	if got := common.GetKubernetesCgroupRootPathWithSubSys("cpu"); !reflect.DeepEqual(got, want) {
		t.Errorf("getAbsCgroupRootPaths() \n got = %v, \n want = %v\n", got, want)
	}

	common.InitKubernetesCGroupPath(common.CgroupTypeSystemd, []string{"/kubepods/test.slice"})

	want = []string{
		"/sys/fs/cgroup/cpu/kubepods.slice",
		"/sys/fs/cgroup/cpu/kubepods.slice/kubepods-besteffort.slice",
		"/sys/fs/cgroup/cpu/kubepods.slice/kubepods-burstable.slice",
		"/sys/fs/cgroup/cpu/kubepods/test.slice",
	}

	if got := common.GetKubernetesCgroupRootPathWithSubSys("cpu"); !reflect.DeepEqual(got, want) {
		t.Errorf("getAbsCgroupRootPaths() \n got = %v, \n want = %v\n", got, want)
	}
}

func TestPodFetcherImpl_getKataCgroupPathSuffix(t *testing.T) {
	t.Parallel()

	type fields struct {
		podUid            string
		containerId       string
		containerIdToInfo map[string]map[string]string
	}

	tests := []struct {
		name                 string
		fields               fields
		wantCgroupPathSuffix string
		wantErr              bool
	}{
		{
			name: "Cannot find container info",
			fields: fields{
				podUid:      "12345678",
				containerId: "invalidContainerId",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testKataJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantErr:              true,
		},
		{
			name: "Can find container info but cannot unmarshal json",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"invalidField": testKataJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantErr:              true,
		},
		{
			name: "Empty sandbox id",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testInvalidJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantErr:              true,
		},
		{
			name: "Not kata container",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testNonKataJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "",
			wantErr:              true,
		},
		{
			name: "Can get the kata cgroup path suffix",
			fields: fields{
				podUid:      "12345678",
				containerId: "container1234",
				containerIdToInfo: map[string]map[string]string{
					"container1234": {
						"info": testKataJsonInfo,
					},
				},
			},
			wantCgroupPathSuffix: "pod12345678/kata_12345678",
			wantErr:              false,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			podFetcher := &podFetcherImpl{
				runtimePodFetcher: &runtimePodFetcherStub{
					containerIdToInfo: tt.fields.containerIdToInfo,
				},
			}
			pathSuffix, err := podFetcher.getKataCgroupPathSuffix(tt.fields.podUid, tt.fields.containerId)
			if (err != nil) != tt.wantErr {
				t.Errorf("getKataCgroupPathSuffix() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if pathSuffix != tt.wantCgroupPathSuffix {
				t.Errorf("getKataCgroupPathSuffix() pathSuffix = %v, want %v", pathSuffix, tt.wantCgroupPathSuffix)
			}
		})
	}
}
