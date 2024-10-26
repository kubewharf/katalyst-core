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

package oom

import (
	"context"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8sfake "k8s.io/client-go/kubernetes/fake"
)

func TestCleanOOMRecord(t *testing.T) {
	oomRecordsList := []PodOOMRecorder{
		{
			OOMRecordMaxNumber: 4,
			cache: []OOMRecord{
				{
					OOMAt: time.Now().Add(-140 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-150 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-150 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-150 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-150 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-160 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-170 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-180 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-190 * time.Hour),
				},
			},
		},
		{
			OOMRecordMaxNumber: 4,
			cache: []OOMRecord{
				{
					OOMAt: time.Now().Add(-150 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-140 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-130 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-120 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-110 * time.Hour),
				},
			},
		},
		{
			OOMRecordMaxNumber: 4,
			cache: []OOMRecord{
				{
					OOMAt: time.Now().Add(-170 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-180 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-190 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-190 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-200 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-210 * time.Hour),
				},
			},
		},
		{
			OOMRecordMaxNumber: 4,
			cache: []OOMRecord{
				{
					OOMAt: time.Now().Add(-160 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-170 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-180 * time.Hour),
				},
			},
		},
		{
			OOMRecordMaxNumber: 4,
			cache: []OOMRecord{
				{
					OOMAt: time.Now().Add(-170 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-180 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-190 * time.Hour),
				},
			},
		},
		{
			OOMRecordMaxNumber: 4,
			cache: []OOMRecord{
				{
					OOMAt: time.Now().Add(-160 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-150 * time.Hour),
				},
				{
					OOMAt: time.Now().Add(-140 * time.Hour),
				},
			},
		},
		{
			OOMRecordMaxNumber: 4,
			cache:              []OOMRecord{},
		},
	}
	for index := range oomRecordsList {
		splitTimePoints := time.Now().Add(-DataRetentionHour * time.Hour)
		oomRecordsList[index].cleanOOMRecord()
		if len(oomRecordsList[index].cache) > oomRecordsList[index].OOMRecordMaxNumber {
			t.Errorf("Expected oomRecordsList length to be less than or equal to %d, but it is actually %d",
				oomRecordsList[index].OOMRecordMaxNumber, len(oomRecordsList[index].cache))
		}
		for _, record := range oomRecordsList[index].cache {
			if record.OOMAt.Before(splitTimePoints) {
				t.Errorf("Expected oomAt to be greater than %v, but it is actually %v", splitTimePoints, record.OOMAt)
			}
		}
	}
}

func TestListOOMRecordsFromConfigmap(t *testing.T) {
	dummyClient := k8sfake.NewSimpleClientset().CoreV1()
	dummyPodOOMRecorder := PodOOMRecorder{
		Client: dummyClient,
	}
	oomRecords, _ := dummyPodOOMRecorder.ListOOMRecordsFromConfigmap()
	if len(oomRecords) > 0 {
		t.Errorf("Expected oomRecords length is zero, but actual oomRecords length is %v", len(oomRecords))
	}
	oomConfigMap := &v1.ConfigMap{
		Data: map[string]string{
			ConfigMapDataOOMRecord: `[{"Namespace":"dummyNamespace","Pod":"dummyPod","Container":"dummyContainer","Memory":"600Mi","OOMAt":"2023-08-07T16:45:50+08:00"},{"Namespace":"dummyNamespace","Pod":"dummyPod","Container":"dummyContainer","Memory":"600Mi","OOMAt":"2023-08-07T16:46:07+08:00"}]`,
		},
	}
	oomConfigMap.SetName(ConfigMapOOMRecordName)
	oomConfigMap.SetNamespace(ConfigMapOOMRecordNameSpace)
	_, err := dummyPodOOMRecorder.Client.ConfigMaps(ConfigMapOOMRecordNameSpace).Create(context.TODO(), oomConfigMap, metav1.CreateOptions{})
	if err != nil {
		t.Errorf("Failed to create oom record: %v", err)
	}
	oomRecords, err = dummyPodOOMRecorder.ListOOMRecordsFromConfigmap()
	if err != nil || len(oomRecords) != 2 {
		t.Errorf("Expected oomRecords length is 2 and err is nil, but actual oomRecords length is %v and err is %v", len(oomRecords), err)
	}
}

func TestUpdateOOMRecordCache(t *testing.T) {
	now := time.Now()
	dummyClient := k8sfake.NewSimpleClientset().CoreV1()
	podOOMRecorderList := []PodOOMRecorder{
		{
			Client: dummyClient,
			cache: []OOMRecord{
				{
					Namespace: "dummyNamespace1",
					Pod:       "dummyPod1",
					Container: "dummyContainer1",
					Memory:    resource.MustParse("6Gi"),
					OOMAt:     now,
				},
			},
		},
		{
			Client: dummyClient,
			cache: []OOMRecord{
				{
					Namespace: "dummyNamespace",
					Pod:       "dummyPod",
					Container: "dummyContainer",
					Memory:    resource.MustParse("6Gi"),
					OOMAt:     now,
				},
			},
		},
		{
			Client: dummyClient,
			cache: []OOMRecord{
				{
					Namespace: "dummyNamespace",
					Pod:       "dummyPod",
					Container: "dummyContainer",
					Memory:    resource.MustParse("6Gi"),
					OOMAt:     now.Add(1 * time.Hour),
				},
			},
		},
		{
			Client: dummyClient,
			cache: []OOMRecord{
				{
					Namespace: "dummyNamespace",
					Pod:       "dummyPod",
					Container: "dummyContainer",
					Memory:    resource.MustParse("7Gi"),
					OOMAt:     now.Add(1 * time.Hour),
				},
			},
		},
		{
			Client: dummyClient,
			cache: []OOMRecord{
				{
					Namespace: "dummyNamespace",
					Pod:       "dummyPod",
					Container: "dummyContainer",
					Memory:    resource.MustParse("5Gi"),
					OOMAt:     now.Add(1 * time.Hour),
				},
			},
		},
	}
	oomRecord := OOMRecord{
		Namespace: "dummyNamespace",
		Pod:       "dummyPod",
		Container: "dummyContainer",
		Memory:    resource.MustParse("6Gi"),
		OOMAt:     now,
	}
	resultList := []bool{true, false, true, false, true}
	for index := range podOOMRecorderList {
		isUpdated := podOOMRecorderList[index].updateOOMRecordCache(oomRecord)
		if isUpdated != resultList[index] {
			t.Errorf("Expected isUpdated %v, but it is actually %v", resultList[index], isUpdated)
		}
	}
}

func TestUpdateOOMRecordConfigMap(t *testing.T) {
	dummyClient := k8sfake.NewSimpleClientset().CoreV1()
	oomConfigMap := &v1.ConfigMap{
		Data: map[string]string{
			ConfigMapDataOOMRecord: `[]`,
		},
	}
	oomConfigMap.SetName(ConfigMapOOMRecordName)
	oomConfigMap.SetNamespace(ConfigMapOOMRecordNameSpace)
	dummyPodOOMRecorder := PodOOMRecorder{
		Client: dummyClient,
		cache: []OOMRecord{
			{
				Namespace: "dummyNamespace",
				Pod:       "dummyPod",
				Container: "dummyContainer",
				Memory:    resource.MustParse("600Mi"),
				OOMAt:     time.Date(2012, time.March, 4, 0, 0, 0, 0, time.UTC),
			},
		},
	}
	_, err := dummyClient.ConfigMaps(ConfigMapOOMRecordNameSpace).Create(context.TODO(), oomConfigMap, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("Failed to create oom record: %v", err)
	}

	if err := dummyPodOOMRecorder.updateOOMRecordConfigMap(); err != nil {
		t.Errorf("Expected the configMap was successfully created,but actually an error:%v occurred.", err)
	}
	oomRecordList, _ := dummyPodOOMRecorder.ListOOMRecordsFromConfigmap()
	for index := range oomRecordList {
		if dummyPodOOMRecorder.cache[index] != oomRecordList[index] {
			t.Errorf("Expected OOMRecord value is %v, actually OOMRecord value is %v",
				dummyPodOOMRecorder.cache[index], oomRecordList[index])
		}
	}
	if err := dummyPodOOMRecorder.Client.ConfigMaps(ConfigMapOOMRecordNameSpace).Delete(context.TODO(), oomConfigMap.GetName(), metav1.DeleteOptions{}); err != nil {
		t.Errorf("Delete ConfigMaps meet error: %v", err)
	}

	if _, err := dummyPodOOMRecorder.Client.ConfigMaps(ConfigMapOOMRecordNameSpace).Create(context.TODO(), oomConfigMap, metav1.CreateOptions{}); err != nil {
		t.Errorf("Create ConfigMaps meet error: %v", err)
	}
	err = dummyPodOOMRecorder.updateOOMRecordConfigMap()
	if err != nil {
		t.Errorf("Expected the configMap was successfully updated,but actually an error:%v occurred.", err)
	}
	oomRecordList, _ = dummyPodOOMRecorder.ListOOMRecordsFromConfigmap()
	for index := range oomRecordList {
		if dummyPodOOMRecorder.cache[index] != oomRecordList[index] {
			t.Errorf("Expected OOMRecord value is %v, actually OOMRecord value is %v",
				dummyPodOOMRecorder.cache[index], oomRecordList[index])
		}
	}
}
