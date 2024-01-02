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

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/metric/provisioner/malachite/types"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
)

func TestGetPodContainerStats(t *testing.T) {
	cgroupData := map[string]*types.MalachiteCgroupResponse{
		"podp-uid1/p1-c-uid1": {
			Status: 0,
			Data: types.CgroupDataInner{
				CgroupType:      "V1",
				SubSystemGroups: subSystemGroupsV1Data,
			},
		},
		"podp-uid1/p1-c-uid2": {
			Status: 0,
			Data: types.CgroupDataInner{
				CgroupType:      "V2",
				SubSystemGroups: subSystemGroupsV2Data,
			},
		},
		"podp-uid2/p2-c-uid1": {
			Status: 0,
			Data: types.CgroupDataInner{
				CgroupType:      "V1",
				SubSystemGroups: subSystemGroupsV1Data,
			},
		},
		"podp-uid3/p3-c-uid1": {
			Status: 0,
			Data: types.CgroupDataInner{
				CgroupType:      "V2",
				SubSystemGroups: subSystemGroupsV2Data,
			},
		},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Response == nil {
			r.Response = &http.Response{}
		}
		r.Response.StatusCode = http.StatusOK

		q := r.URL.Query()
		rPath := q.Get(CgroupPathParamKey)
		for path, info := range cgroupData {
			if path == rPath {
				data, _ := json.Marshal(info)
				_, _ = w.Write(data)
				return
			}
		}

		r.Response.StatusCode = http.StatusBadRequest
	}))
	defer server.Close()

	pods := []*v1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "p-name1",
				UID:  apitypes.UID("p-uid1"),
			},
			Status: v1.PodStatus{
				ContainerStatuses: []v1.ContainerStatus{
					{
						Name:        "p1-c-name1",
						ContainerID: "docker://p1-c-uid1",
					},
					{
						Name:        "p1-c-name2",
						ContainerID: "containerd://p1-c-uid2",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "p-name2",
				UID:  apitypes.UID("p-uid2"),
			},
			Status: v1.PodStatus{
				ContainerStatuses: []v1.ContainerStatus{
					{
						Name:        "p2-c-name1",
						ContainerID: "p2-c-uid1",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "p-name3",
				UID:  apitypes.UID("p-uid3"),
			},
			Status: v1.PodStatus{
				ContainerStatuses: []v1.ContainerStatus{
					{
						Name:        "p3-c-name1",
						ContainerID: "containerd://p3-c-uid1",
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "p-name4",
				UID:  apitypes.UID("p-uid4"),
			},
			Status: v1.PodStatus{
				ContainerStatuses: []v1.ContainerStatus{
					{
						Name:        "p4-c-name1",
						ContainerID: "containerd://p4-c-uid1",
					},
				},
			},
		},
	}
	fetcher := &pod.PodFetcherStub{PodList: pods}

	malachiteClient := NewMalachiteClient(fetcher)
	malachiteClient.SetURL(map[string]string{
		CgroupResource: server.URL,
	})

	stats, err := malachiteClient.GetAllPodContainersStats(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 0, len(stats))

	relativePathFunc := func(podUID, containerId string) (string, error) {
		return path.Join(fmt.Sprintf("%s%s", common.PodCgroupPathPrefix, podUID), containerId), nil
	}
	malachiteClient.relativePathFunc = &relativePathFunc

	stats, err = malachiteClient.GetAllPodContainersStats(context.Background())
	assert.NoError(t, err)
	assert.Equal(t, 3, len(stats))

	assert.NotNil(t, stats["p-uid1"]["p1-c-name1"].V1)
	assert.Nil(t, stats["p-uid1"]["p1-c-name1"].V2)

	assert.NotNil(t, stats["p-uid1"]["p1-c-name2"].V2)
	assert.Nil(t, stats["p-uid1"]["p1-c-name2"].V1)

	assert.NotNil(t, stats["p-uid2"]["p2-c-name1"].V1)
	assert.Nil(t, stats["p-uid2"]["p2-c-name1"].V2)

	assert.NotNil(t, stats["p-uid3"]["p3-c-name1"].V2)
	assert.Nil(t, stats["p-uid3"]["p3-c-name1"].V1)
}
