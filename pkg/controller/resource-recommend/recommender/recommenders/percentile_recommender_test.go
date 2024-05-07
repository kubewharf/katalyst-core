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

package recommenders

import (
	"context"
	"reflect"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/oom"
	datasourcetypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/datasource"
	customtypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
	recommendationtypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/recommendation"
)

func TestRecommend(t *testing.T) {
	recommendation1 := &recommendationtypes.Recommendation{
		NamespacedName: types.NamespacedName{
			Name:      "name1",
			Namespace: "namespace1",
		},
		Config: recommendationtypes.Config{
			Containers: []recommendationtypes.Container{
				{
					ContainerName: "container1",
					ContainerConfigs: []recommendationtypes.ContainerConfig{
						{
							ControlledResource:    v1.ResourceCPU,
							ResourceBufferPercent: 10,
						},
						{
							ControlledResource:    v1.ResourceMemory,
							ResourceBufferPercent: 10,
						},
					},
				},
			},
			TargetRef: v1alpha1.CrossVersionObjectReference{
				Kind: "deployment",
				Name: "workload1",
			},
		},
	}

	r := &PercentileRecommender{
		DataProcessor: dummyDataProcessor{},
		OomRecorder:   dummyOomRecorder{},
	}
	err := r.Recommend(recommendation1)
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if recommendation1.Recommendations[0].Requests.Target.Cpu().String() != "1100" || recommendation1.Recommendations[0].Requests.Target.Memory().String() != "2Ki" {
		t.Errorf("Recommendations mismatch.")
	}
}

func TestGetCpuTargetPercentileEstimationWithUsageBuffer(t *testing.T) {
	recommender := &PercentileRecommender{
		DataProcessor: dummyDataProcessor{},
		OomRecorder:   dummyOomRecorder{},
	}
	taskKey := &processortypes.ProcessKey{
		ResourceRecommendNamespacedName: types.NamespacedName{
			Name:      "name1",
			Namespace: "namespace1",
		},
		Metric: &datasourcetypes.Metric{
			Namespace:     "namespace1",
			Kind:          "deployment1",
			WorkloadName:  "workload1",
			ContainerName: "container1",
			Resource:      v1.ResourceCPU,
		},
	}
	resourceBufferPercentage := 0.1
	cpuQuantity, err := recommender.getCpuTargetPercentileEstimationWithUsageBuffer(taskKey, resourceBufferPercentage)
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}

	expectedCpuQuantity := resource.NewMilliQuantity(int64(1100*1000), resource.DecimalSI) // 设置期望的CPU数量
	if !cpuQuantity.Equal(*expectedCpuQuantity) {
		t.Errorf("Expected cpu quantity %s, but got %s", cpuQuantity.String(), expectedCpuQuantity.String())
	}
}

func TestGetMemTargetPercentileEstimationWithUsageBuffer(t *testing.T) {
	recommender := &PercentileRecommender{
		DataProcessor: dummyDataProcessor{},
		OomRecorder:   dummyOomRecorder{},
	}
	taskKey := &processortypes.ProcessKey{
		ResourceRecommendNamespacedName: types.NamespacedName{
			Name:      "name1",
			Namespace: "namespace1",
		},
		Metric: &datasourcetypes.Metric{
			Namespace:     "namespace1",
			Kind:          "deployment1",
			WorkloadName:  "workload1",
			ContainerName: "container1",
			Resource:      v1.ResourceMemory,
		},
	}
	resourceBufferPercentage := 0.1
	memQuantity, err := recommender.getMemTargetPercentileEstimationWithUsageBuffer(taskKey, resourceBufferPercentage)
	if err != nil {
		t.Errorf("Expected no error, but got: %v", err)
	}

	expectedMemQuantity := resource.NewQuantity((1100/1024+1)*1024, resource.BinarySI) // 设置期望的内存数量
	if !memQuantity.Equal(*expectedMemQuantity) {
		t.Errorf("Expected memory quantity %s, but got %s", expectedMemQuantity.String(), memQuantity.String())
	}
}

func TestScaleOnOOM(t *testing.T) {
	oomRecords := []oom.OOMRecord{
		{
			Pod:       "workload-name-pod-1",
			Container: "container-1",
			Namespace: "namespace-1",
			OOMAt:     time.Now(),
			Memory:    *resource.NewQuantity(1024, resource.BinarySI),
		},
		{
			Pod:       "workload-name-pod-2",
			Container: "container-2",
			Namespace: "namespace-2",
			OOMAt:     time.Now().Add(-time.Hour * 24 * 8), // too old event
			Memory:    *resource.NewQuantity(2048, resource.BinarySI),
		},
	}

	r := &PercentileRecommender{}
	namespace := "namespace-1"
	workloadName := "workload-name"
	containerName := "container-1"

	quantityPointer := r.ScaleOnOOM(oomRecords, namespace, workloadName, containerName)

	if quantityPointer == nil {
		t.Errorf("Expected non-nil quantity, got nil")
		return
	}
	quantity := *quantityPointer

	expectedValuePointer := r.getMemQuantity(float64(oomRecords[0].Memory.Value()) + OOMMinBumpUp)
	if expectedValuePointer == nil {
		t.Errorf("got expectedValuePointer is nil")
		return
	}
	expectedValue := *expectedValuePointer
	if !quantity.Equal(expectedValue) {
		t.Errorf("Expected value %s, got %s", expectedValue.String(), quantity.String())
		return
	}
}

type dummyDataProcessor struct{}

func (d dummyDataProcessor) Run(_ context.Context) {
	return
}

func (d dummyDataProcessor) Register(_ *processortypes.ProcessConfig) *customtypes.CustomError {
	return nil
}

func (d dummyDataProcessor) Cancel(_ *processortypes.ProcessKey) *customtypes.CustomError {
	return nil
}

func (d dummyDataProcessor) QueryProcessedValues(_ *processortypes.ProcessKey) (float64, error) {
	return 1000, nil
}

type dummyOomRecorder struct{}

func (d dummyOomRecorder) ListOOMRecords() []oom.OOMRecord {
	return nil
}

func (d dummyOomRecorder) ScaleOnOOM(_ []oom.OOMRecord, _, _, _ string) *resource.Quantity {
	return nil
}

func TestPercentileRecommender_getMemQuantity(t *testing.T) {
	tests := []struct {
		name                string
		memRecommendedValue float64
		wantQuantity        *resource.Quantity
	}{
		{
			name:                "testKi",
			memRecommendedValue: 1100,
			wantQuantity:        resource.NewQuantity(2*1024, resource.BinarySI),
		},
		{
			name:                "testMi",
			memRecommendedValue: 2 * 1024 * 1024,
			wantQuantity:        resource.NewQuantity(2*1024*1024, resource.BinarySI),
		},
		{
			name:                "testMiNotDivisible",
			memRecommendedValue: 2.66 * 1024 * 1024,
			wantQuantity:        resource.NewQuantity(3*1024*1024, resource.BinarySI),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &PercentileRecommender{}
			if gotQuantity := r.getMemQuantity(tt.memRecommendedValue); !reflect.DeepEqual(gotQuantity, tt.wantQuantity) {
				t.Errorf("getMemQuantity() = %v, want %v", gotQuantity, tt.wantQuantity)
			}
		})
	}
}
