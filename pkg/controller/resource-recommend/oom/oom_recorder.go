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
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	ConfigMapOOMRecordName      = "oom-record"
	ConfigMapDataOOMRecord      = "oom-data"
	ConfigMapOOMRecordNameSpace = "kube-system"
	CacheCleanTimeDurationHour  = 12
	DataRetentionHour           = 168
)

type Recorder interface {
	ListOOMRecords() []OOMRecord
}

type OOMRecord struct {
	Namespace string
	Pod       string
	Container string
	Memory    resource.Quantity
	OOMAt     time.Time
}

type PodOOMRecorder struct {
	client.Client

	mu sync.Mutex

	OOMRecordMaxNumber int
	cache              []OOMRecord
	Queue              workqueue.Interface
}

func (r *PodOOMRecorder) initOOMCacheFromConfigmap() {
	r.mu.Lock()
	defer r.mu.Unlock()

	oomRecords, err := r.ListOOMRecordsFromConfigmap()
	if err != nil {
		// TODO: add monitor metric
		klog.ErrorS(err, "init cache from configmap failed")
	}
	r.cache = oomRecords
}

func (r *PodOOMRecorder) ListOOMRecords() []OOMRecord {
	return r.cache
}

func (r *PodOOMRecorder) cleanOOMRecord() {
	r.mu.Lock()
	defer r.mu.Unlock()
	oomCache := r.ListOOMRecords()
	sort.Slice(oomCache, func(i, j int) bool {
		return oomCache[i].OOMAt.Before(oomCache[j].OOMAt)
	})
	now := time.Now()
	index := 0
	for i := len(oomCache) - 1; i >= 0; i-- {
		if oomCache[i].OOMAt.Before(now.Add(-DataRetentionHour * time.Hour)) {
			break
		}
		index++
		if index >= r.OOMRecordMaxNumber {
			break
		}
	}
	r.cache = oomCache[len(oomCache)-index:]
}

func (r *PodOOMRecorder) updateOOMRecordCache(oomRecord OOMRecord) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	oomCache := r.ListOOMRecords()
	if oomCache == nil {
		oomCache = []OOMRecord{}
	}

	isFound := false
	isUpdated := false
	for i := range oomCache {
		if oomCache[i].Namespace == oomRecord.Namespace && oomCache[i].Pod == oomRecord.Pod && oomCache[i].Container == oomRecord.Container {
			if oomRecord.Memory.Value() >= oomCache[i].Memory.Value() && !oomRecord.OOMAt.Equal(oomCache[i].OOMAt) {
				oomCache[i].Memory = oomRecord.Memory
				oomCache[i].OOMAt = oomRecord.OOMAt
				isUpdated = true
			}
			isFound = true
			break
		}
	}

	if !isFound {
		oomCache = append(oomCache, oomRecord)
		isUpdated = true
	}
	if isUpdated {
		r.cache = oomCache
	}
	return isUpdated
}

func (r *PodOOMRecorder) updateOOMRecordConfigMap() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	oomCache := r.ListOOMRecords()
	cacheData, err := json.Marshal(oomCache)
	if err != nil {
		return err
	}
	oomConfigMap := &v1.ConfigMap{}
	err = r.Client.Get(context.TODO(), types.NamespacedName{
		Namespace: ConfigMapOOMRecordNameSpace,
		Name:      ConfigMapOOMRecordName,
	}, oomConfigMap)
	if err != nil {
		if client.IgnoreNotFound(err) != nil {
			return err
		}
		oomConfigMap.Name = ConfigMapOOMRecordName
		oomConfigMap.Namespace = ConfigMapOOMRecordNameSpace
		oomConfigMap.Data = map[string]string{
			ConfigMapDataOOMRecord: string(cacheData),
		}
		return r.Client.Create(context.TODO(), oomConfigMap)
	}
	oomConfigMap.Data = map[string]string{
		ConfigMapDataOOMRecord: string(cacheData),
	}
	return r.Client.Update(context.TODO(), oomConfigMap)
}

func (r *PodOOMRecorder) Run(stopCh <-chan struct{}) error {
	r.initOOMCacheFromConfigmap()
	cleanTicker := time.NewTicker(time.Duration(CacheCleanTimeDurationHour) * time.Hour)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				if r := recover(); r != nil {
					err := errors.Errorf("Run clean oom recorder panic: %v", r.(error))
					klog.Error(err)
					panic(err)
				}
			}
		}()
		for range cleanTicker.C {
			r.cleanOOMRecord()
		}
	}()
	for {
		select {
		case <-stopCh:
			return nil
		default:
		}

		record, shutdown := r.Queue.Get()
		if shutdown {
			return errors.New("queue of OOMRecord recorder is shutting down ! ")
		}
		oomRecord, ok := record.(OOMRecord)
		if !ok {
			klog.Error("type conversion failed")
			r.Queue.Done(record)
			continue
		}
		isUpdated := r.updateOOMRecordCache(oomRecord)
		if !isUpdated {
			r.Queue.Done(record)
			continue
		}

		err := r.updateOOMRecordConfigMap()
		if err != nil {
			klog.ErrorS(err, "Update oomRecord failed")
		}
		r.Queue.Done(record)
	}
}

func (r *PodOOMRecorder) ListOOMRecordsFromConfigmap() ([]OOMRecord, error) {
	oomConfigMap := &v1.ConfigMap{}
	err := r.Client.Get(context.TODO(), types.NamespacedName{
		Namespace: ConfigMapOOMRecordNameSpace,
		Name:      ConfigMapOOMRecordName,
	}, oomConfigMap)
	if err != nil {
		return nil, client.IgnoreNotFound(err)
	}
	oomRecords := make([]OOMRecord, 0)
	err = json.Unmarshal([]byte(oomConfigMap.Data[ConfigMapDataOOMRecord]), &oomRecords)
	return oomRecords, err
}
