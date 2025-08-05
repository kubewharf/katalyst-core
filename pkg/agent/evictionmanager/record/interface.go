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

package record

import (
	"context"

	v1 "k8s.io/api/core/v1"
)

type EvictionRecordManager interface {
	GetEvictionRecords(ctx context.Context, evictionPods []*v1.Pod) (map[string]EvictionRecord, error)
	Run(ctx context.Context)
}

type EvictionRecord struct {
	HasPDB             bool
	Buckets            Buckets
	DisruptionsAllowed int32
	CurrentHealthy     int32
	DesiredHealthy     int32
	ExpectedPods       int32
}

type Buckets struct {
	List []Bucket
}

type Bucket struct {
	Time     int64
	Count    int64
	Duration int64
}

type DummyEvictionRecordManager struct{}

var _ EvictionRecordManager = &DummyEvictionRecordManager{}

func (d *DummyEvictionRecordManager) GetEvictionRecords(_ context.Context, _ []*v1.Pod) (map[string]EvictionRecord, error) {
	return map[string]EvictionRecord{}, nil
}

func (d *DummyEvictionRecordManager) Run(_ context.Context) {
}
