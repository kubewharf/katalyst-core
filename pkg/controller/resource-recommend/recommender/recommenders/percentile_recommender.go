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
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	vpamodel "k8s.io/autoscaler/vertical-pod-autoscaler/pkg/recommender/model"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/oom"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor"
	"github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/recommender"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
	recommendationtype "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/recommendation"
)

type PercentileRecommender struct {
	recommender.Recommender
	DataProcessor processor.Processor
	OomRecorder   oom.Recorder
}

const (
	// OOMBumpUpRatio specifies how much memory will be added after observing OOM.
	OOMBumpUpRatio float64 = 1.2
	// OOMMinBumpUp specifies minimal increase of memory after observing OOM.
	OOMMinBumpUp float64 = 100 * 1024 * 1024 // 100MB
)

// NewPercentileRecommender returns a
func NewPercentileRecommender(DataProcessor processor.Processor, OomRecorder oom.Recorder) *PercentileRecommender {
	return &PercentileRecommender{
		DataProcessor: DataProcessor,
		OomRecorder:   OomRecorder,
	}
}

func (r *PercentileRecommender) Recommend(recommendation *recommendationtype.Recommendation) *errortypes.CustomError {
	klog.InfoS("starting recommenders process", "recommendationConfig", recommendation.Config)
	for _, container := range recommendation.Config.Containers {
		containerRecommendation := v1alpha1.ContainerResources{
			ContainerName: container.ContainerName,
		}
		requests := v1alpha1.ContainerResourceList{
			Target: map[v1.ResourceName]resource.Quantity{},
		}
		for _, containerConfig := range container.ContainerConfigs {
			taskKey := processortypes.GetProcessKey(recommendation.NamespacedName, recommendation.Config.TargetRef, container.ContainerName, containerConfig.ControlledResource)
			switch containerConfig.ControlledResource {
			case v1.ResourceCPU:
				cpuQuantity, err := r.getCpuTargetPercentileEstimationWithUsageBuffer(&taskKey, float64(containerConfig.ResourceBufferPercent)/100)
				if err != nil {
					return errortypes.RecommendationNotReadyError(err.Error())
				}
				klog.InfoS("got recommended cpu for container", "recommendedCPU", cpuQuantity.String(), "container", container.ContainerName)
				requests.Target[v1.ResourceCPU] = *cpuQuantity
			case v1.ResourceMemory:
				memQuantity, err := r.getMemTargetPercentileEstimationWithUsageBuffer(&taskKey, float64(containerConfig.ResourceBufferPercent)/100)
				if err != nil {
					return errortypes.RecommendationNotReadyError(err.Error())
				}
				requests.Target[v1.ResourceMemory] = *memQuantity
			}
		}
		containerRecommendation.Requests = &requests
		recommendation.Recommendations = append(recommendation.Recommendations, containerRecommendation)
	}
	klog.InfoS("recommenders process done", "recommendation", general.StructToString(recommendation.Recommendations))
	return nil
}

func (r *PercentileRecommender) ScaleOnOOM(oomRecords []oom.OOMRecord, namespace string, workloadName string, containerName string) *resource.Quantity {
	klog.InfoS("scaling on oom for namespace, workload, container", "namespace", namespace, "workload", workloadName, "container", containerName)
	var oomRecord *oom.OOMRecord
	for _, record := range oomRecords {
		// use oomRecord for all pods in workload
		if strings.HasPrefix(record.Pod, workloadName) && containerName == record.Container && namespace == record.Namespace {
			oomRecord = &record
			break
		}
	}

	// ignore too old oom events
	if oomRecord != nil && time.Since(oomRecord.OOMAt) <= (time.Hour*24*7) {
		memoryOOM := oomRecord.Memory.Value()
		var memoryNeeded vpamodel.ResourceAmount
		memoryNeeded = vpamodel.ResourceAmountMax(vpamodel.ResourceAmount(memoryOOM)+vpamodel.MemoryAmountFromBytes(OOMMinBumpUp),
			vpamodel.ScaleResource(vpamodel.ResourceAmount(memoryOOM), OOMBumpUpRatio))

		return r.getMemQuantity(float64(memoryNeeded))
	}

	return nil
}

func (r *PercentileRecommender) getCpuTargetPercentileEstimationWithUsageBuffer(taskKey *processortypes.ProcessKey, resourceBufferPercentage float64) (quantity *resource.Quantity, err error) {
	klog.InfoS("getting cpu estimation for namespace, workload, container, with resource buffer", "namespace", taskKey.Namespace, "workload", taskKey.WorkloadName, "container", taskKey.ContainerName, "resourceBuffer", resourceBufferPercentage)
	cpuRecommendedValue, err := r.DataProcessor.QueryProcessedValues(taskKey)
	if err != nil {
		return nil, err
	}
	klog.InfoS("got cpu recommended value from processor", "cpuRecommendedValue", cpuRecommendedValue)
	// scale cpu resource based on usageBuffer
	cpuRecommendedValue = cpuRecommendedValue * (1 + resourceBufferPercentage)
	klog.InfoS("scaled cpu recommended value for container", "container", taskKey.ContainerName, "resourceBuffer", resourceBufferPercentage, "cpuRecommendedValue", cpuRecommendedValue)
	cpuQuantity := resource.NewMilliQuantity(int64(cpuRecommendedValue*1000), resource.DecimalSI)
	return cpuQuantity, nil
}

func (r *PercentileRecommender) getMemTargetPercentileEstimationWithUsageBuffer(taskKey *processortypes.ProcessKey, resourceBufferPercentage float64) (quantity *resource.Quantity, err error) {
	klog.InfoS("getting mem estimation for namespace, workload, container, with resource buffer", "namespace", taskKey.Namespace, "workload", taskKey.WorkloadName, "container", taskKey.ContainerName, "resourceBuffer", resourceBufferPercentage)
	memRecommendedValue, err := r.DataProcessor.QueryProcessedValues(taskKey)
	if err != nil {
		return nil, err
	}
	klog.InfoS("got mem recommended value from processor", "memRecommendedValue", memRecommendedValue)
	// scale mem resource based on usageBuffer
	memRecommendedValue = memRecommendedValue * (1 + resourceBufferPercentage)
	klog.InfoS("scaled mem recommended value for container", "container", taskKey.ContainerName, "resourceBuffer", resourceBufferPercentage, "memRecommendedValue", memRecommendedValue)
	memQuantity := r.getMemQuantity(memRecommendedValue)
	klog.InfoS("got recommended memory for container", "container", taskKey.ContainerName, "memory", memQuantity.String())
	oomRecords := r.OomRecorder.ListOOMRecords()
	oomScaledMem := r.ScaleOnOOM(oomRecords, taskKey.Namespace, taskKey.WorkloadName, taskKey.ContainerName)
	if oomScaledMem != nil && !oomScaledMem.IsZero() && oomScaledMem.Cmp(*memQuantity) > 0 {
		klog.InfoS("container using oomProtect Memory", "container", taskKey.ContainerName, "oomScaledMem", oomScaledMem.String())
		memQuantity = oomScaledMem
	}
	return memQuantity, nil
}

func (r *PercentileRecommender) getMemQuantity(memRecommendedValue float64) (quantity *resource.Quantity) {
	scale := int64(1)
	quotient := int64(memRecommendedValue)
	remainder := int64(0)
	for scale < 1024*1024 {
		if quotient < 1024 {
			break
		}
		scale *= 1024
		quotient = int64(memRecommendedValue) / scale
		remainder = int64(memRecommendedValue) % scale
	}
	if remainder == 0 {
		return resource.NewQuantity(quotient*scale, resource.BinarySI)
	}
	return resource.NewQuantity((quotient+1)*scale, resource.BinarySI)
}
