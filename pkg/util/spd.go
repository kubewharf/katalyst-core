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

package util

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/pkg/errors"
	core "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	apiworkload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/scheme"
	workloadlister "github.com/kubewharf/katalyst-api/pkg/client/listers/workload/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

const (
	spdConfigHashLength = 12
)

/*
 helper functions to get spd-related objects with indexed informer
*/

// GetSPDForWorkload is used to get spd that should manage the given workload
func getSPDFroWorkloadWithIndex(workload *unstructured.Unstructured, spdIndexer cache.Indexer) ([]*apiworkload.ServiceProfileDescriptor, error) {
	objs, err := spdIndexer.ByIndex(consts.TargetReferenceIndex, generateWorkloadReferenceKey(workload))
	if err != nil {
		return nil, errors.Wrapf(err, "spd for workload %s/%s not exist", workload.GetNamespace(), workload.GetName())
	}

	spdList := make([]*apiworkload.ServiceProfileDescriptor, 0, len(objs))
	for _, obj := range objs {
		spd, ok := obj.(*apiworkload.ServiceProfileDescriptor)
		if !ok {
			return nil, fmt.Errorf("invalid spd")
		}
		spdList = append(spdList, spd)
	}
	return spdList, nil
}

/*
 helper functions to build indexed informer for spd-related objects
*/

// SPDTargetReferenceIndex is used to construct informer index for target reference in SPD
func SPDTargetReferenceIndex(obj interface{}) ([]string, error) {
	spd, ok := obj.(*apiworkload.ServiceProfileDescriptor)
	if !ok || spd == nil {
		return nil, fmt.Errorf("failed to reflect a obj to spd")
	}
	return objectTargetReferenceIndex(spd.Spec.TargetRef)
}

/*
 helper functions to get spd-related objects
*/

// GetWorkloadForSPD is used to get workload that should be managed the given spd
func GetWorkloadForSPD(spd *apiworkload.ServiceProfileDescriptor, lister cache.GenericLister) (runtime.Object, error) {
	return lister.ByNamespace(spd.Namespace).Get(spd.Spec.TargetRef.Name)
}

// GetSPDForWorkload is used to get spd that should manage the given workload
// the preference is annotation ---> indexer --> lister
func GetSPDForWorkload(workload *unstructured.Unstructured, spdIndexer cache.Indexer,
	spdLister workloadlister.ServiceProfileDescriptorLister,
) ([]*apiworkload.ServiceProfileDescriptor, []string, error) {
	if !WorkloadSPDEnabled(workload) {
		return nil, nil, fmt.Errorf("workload not enable spd")
	}

	var (
		spdList           []*apiworkload.ServiceProfileDescriptor
		absentSPDNameList []string
		err               error
	)
	spdNameList, specified := GetSPDNameForWorkload(workload)
	for _, spdName := range spdNameList {
		spd, err := spdLister.ServiceProfileDescriptors(workload.GetNamespace()).Get(spdName)
		if err != nil && !apierrors.IsNotFound(err) {
			return nil, nil, err
		} else if err == nil {
			if checkTargetRefMatch(spd.Spec.TargetRef, workload) {
				spdList = append(spdList, spd)
				continue
			}
		}
		absentSPDNameList = append(absentSPDNameList, spdName)
	}

	// if spdList is not empty or specified, we will return it directly
	if len(spdList) > 0 || specified {
		return spdList, absentSPDNameList, nil
	}

	// if spd name is not specified, we will try to find it by indexer and lister
	klog.InfoS("no spd found need by workload", "workload", workload.GetName())
	if spdIndexer != nil {
		if spdList, err := getSPDFroWorkloadWithIndex(workload, spdIndexer); err == nil && len(spdList) > 0 {
			return spdList, nil, nil
		}
	}

	allSPDList, err := spdLister.List(labels.Everything())
	if err != nil {
		return nil, nil, err
	}

	for _, spd := range allSPDList {
		if checkTargetRefMatch(spd.Spec.TargetRef, workload) {
			spdList = append(spdList, spd)
		}
	}

	if len(spdList) > 0 {
		return spdList, nil, nil
	}
	return nil, spdNameList, nil
}

func GetSPDNameForWorkload(workload *unstructured.Unstructured) ([]string, bool) {
	if workload == nil {
		return nil, false
	}
	var specified bool
	spdNameList := make([]string, 0)
	spdNameListStr, ok := workload.GetAnnotations()[apiconsts.WorkloadAnnotationSPDNameList]
	if ok {
		spdNameList = strings.Split(spdNameListStr, ",")
		specified = true
	} else {
		spdNameList = append(spdNameList, workload.GetName())
	}
	return spdNameList, specified
}

// GetSPDForPod is used to get spd that should manage the given pod,
// we'll try to find by annotation for pod, and then go through workload if not exist,
// and we will find it recursively since we don't know in which level the owner will be.
func GetSPDForPod(pod *core.Pod, spdIndexer cache.Indexer, workloadListerMap map[schema.GroupVersionKind]cache.GenericLister,
	spdLister workloadlister.ServiceProfileDescriptorLister, checkSPDMatchWithPod bool,
) (*apiworkload.ServiceProfileDescriptor, error) {
	// different with vpa, we will store spd name in pod name, so we will check whether it's still valid
	if spdName, ok := pod.GetAnnotations()[apiconsts.PodAnnotationSPDNameKey]; ok {
		spd, err := spdLister.ServiceProfileDescriptors(pod.GetNamespace()).Get(spdName)
		if err == nil {
			return spd, nil
		}
	}

	for _, owner := range pod.GetOwnerReferences() {
		gvk := schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind)
		if _, ok := workloadListerMap[gvk]; ok {
			if workloadObj, err := workloadListerMap[gvk].ByNamespace(pod.GetNamespace()).Get(owner.Name); err == nil {
				var targetSPD *apiworkload.ServiceProfileDescriptor
				native.VisitUnstructuredAncestors(workloadObj.(*unstructured.Unstructured),
					workloadListerMap, func(owner *unstructured.Unstructured) bool {
						spdList, absent, err := GetSPDForWorkload(owner, spdIndexer, spdLister)
						if err != nil {
							return true
						}

						if len(spdList) != 1 || len(absent) > 0 {
							klog.Errorf("spd for workload %s/%s is invalid, len %d, absent %v",
								owner.GetNamespace(), owner.GetName(), len(spdList), absent)
							return false
						}

						targetSPD = spdList[0]
						return false
					})
				if targetSPD != nil {
					return targetSPD, nil
				}
			}
		}
	}

	if checkSPDMatchWithPod {
		spdList, err := spdLister.List(labels.Everything())
		if err != nil {
			return nil, err
		}

		for _, spd := range spdList {
			if CheckSPDMatchWithPod(pod, spd, workloadListerMap) {
				return spd, nil
			}
		}
	}

	return nil, apierrors.NewNotFound(apiworkload.Resource(apiworkload.ResourceNameServiceProfileDescriptors), "spd for pod")
}

// GetPodListForSPD is used to get pods that should be managed by the given spd,
// we'll always get through workload
func GetPodListForSPD(spd *apiworkload.ServiceProfileDescriptor, podIndexer cache.Indexer, podLabelIndexKeyList []string,
	workloadListerMap map[schema.GroupVersionResource]cache.GenericLister, podLister corelisters.PodLister,
) ([]*core.Pod, error) {
	gvr, _ := meta.UnsafeGuessKindToResource(schema.FromAPIVersionAndKind(spd.Spec.TargetRef.APIVersion, spd.Spec.TargetRef.Kind))
	workloadLister, ok := workloadListerMap[gvr]
	if !ok {
		return nil, fmt.Errorf("without workload lister for gvr %v", gvr)
	}

	workloadObj, err := GetWorkloadForSPD(spd, workloadLister)
	if err != nil {
		return nil, err
	}

	return native.GetPodListForWorkload(workloadObj, podIndexer, podLabelIndexKeyList, podLister)
}

/*
 helper functions to do validation works
*/

// CheckSPDMatchWithPod checks whether the given pod and spd matches with each other
func CheckSPDMatchWithPod(pod *core.Pod, spd *apiworkload.ServiceProfileDescriptor, workloadListerMap map[schema.GroupVersionKind]cache.GenericLister) bool {
	gvk := schema.FromAPIVersionAndKind(spd.Spec.TargetRef.APIVersion, spd.Spec.TargetRef.Kind)
	if _, ok := workloadListerMap[gvk]; !ok {
		return false
	}

	workloadObj, err := GetWorkloadForSPD(spd, workloadListerMap[gvk])
	if err != nil {
		klog.Errorf("failed to get workload for spd %v: %v", spd.Name, err)
		return false
	}

	if !WorkloadSPDEnabled(workloadObj.(metav1.Object)) {
		return false
	}

	selector, err := native.GetUnstructuredSelector(workloadObj.(*unstructured.Unstructured))
	if err != nil || selector == nil {
		klog.Errorf("failed to get workload selector %v: %v", workloadObj, err)
		return false
	}

	return selector.Matches(labels.Set(pod.Labels))
}

/*
 helper functions to update spd info in-place
*/

func InsertSPDBusinessIndicatorSpec(spec *apiworkload.ServiceProfileDescriptorSpec,
	serviceBusinessIndicatorSpec *apiworkload.ServiceBusinessIndicatorSpec,
) {
	if spec == nil || serviceBusinessIndicatorSpec == nil {
		return
	}

	if spec.BusinessIndicator == nil {
		spec.BusinessIndicator = []apiworkload.ServiceBusinessIndicatorSpec{}
	}

	for i := range spec.BusinessIndicator {
		if spec.BusinessIndicator[i].Name == serviceBusinessIndicatorSpec.Name {
			spec.BusinessIndicator[i].Indicators = serviceBusinessIndicatorSpec.Indicators
			return
		}
	}
	spec.BusinessIndicator = append(spec.BusinessIndicator, *serviceBusinessIndicatorSpec)
}

func InsertSPDSystemIndicatorSpec(spec *apiworkload.ServiceProfileDescriptorSpec,
	serviceSystemIndicatorSpec *apiworkload.ServiceSystemIndicatorSpec,
) {
	if spec == nil || serviceSystemIndicatorSpec == nil {
		return
	}

	if spec.SystemIndicator == nil {
		spec.SystemIndicator = []apiworkload.ServiceSystemIndicatorSpec{}
	}

	for i := range spec.SystemIndicator {
		if spec.SystemIndicator[i].Name == serviceSystemIndicatorSpec.Name {
			spec.SystemIndicator[i].Indicators = serviceSystemIndicatorSpec.Indicators
			return
		}
	}
	spec.SystemIndicator = append(spec.SystemIndicator, *serviceSystemIndicatorSpec)
}

func InsertSPDExtendedIndicatorSpec(spec *apiworkload.ServiceProfileDescriptorSpec,
	serviceExtendedIndicatorSpec *apiworkload.ServiceExtendedIndicatorSpec,
) {
	if spec == nil || serviceExtendedIndicatorSpec == nil {
		return
	}

	if spec.ExtendedIndicator == nil {
		spec.ExtendedIndicator = []apiworkload.ServiceExtendedIndicatorSpec{}
	}

	for i := range spec.ExtendedIndicator {
		if spec.ExtendedIndicator[i].Name == serviceExtendedIndicatorSpec.Name {
			spec.ExtendedIndicator[i].BaselinePercent = serviceExtendedIndicatorSpec.BaselinePercent
			spec.ExtendedIndicator[i].Indicators = serviceExtendedIndicatorSpec.Indicators
			return
		}
	}
	spec.ExtendedIndicator = append(spec.ExtendedIndicator, *serviceExtendedIndicatorSpec)
}

func InsertSPDBusinessIndicatorStatus(status *apiworkload.ServiceProfileDescriptorStatus,
	serviceBusinessIndicatorStatus *apiworkload.ServiceBusinessIndicatorStatus,
) {
	if status == nil || serviceBusinessIndicatorStatus == nil {
		return
	}

	if status.BusinessStatus == nil {
		status.BusinessStatus = []apiworkload.ServiceBusinessIndicatorStatus{}
	}

	for i := range status.BusinessStatus {
		if status.BusinessStatus[i].Name == serviceBusinessIndicatorStatus.Name {
			status.BusinessStatus[i].Current = serviceBusinessIndicatorStatus.Current
			return
		}
	}
	status.BusinessStatus = append(status.BusinessStatus, *serviceBusinessIndicatorStatus)
}

// InsertSPDAggMetricsStatus inserts aggMetrics into spd status.
func InsertSPDAggMetricsStatus(status *apiworkload.ServiceProfileDescriptorStatus,
	serviceAggPodMetrics *apiworkload.AggPodMetrics,
) {
	if status == nil || serviceAggPodMetrics == nil {
		return
	}

	if status.AggMetrics == nil {
		status.AggMetrics = []apiworkload.AggPodMetrics{}
	}

	for i := range status.AggMetrics {
		if status.AggMetrics[i].Scope == serviceAggPodMetrics.Scope && status.AggMetrics[i].Aggregator == serviceAggPodMetrics.Aggregator {
			status.AggMetrics[i].Items = serviceAggPodMetrics.Items
			return
		}
	}
	status.AggMetrics = append(status.AggMetrics, *serviceAggPodMetrics)
}

/*
 helper functions to get the spd hash and the pod's spd name
*/

// GetSPDHash get spd hash from spd annotation
func GetSPDHash(spd *apiworkload.ServiceProfileDescriptor) string {
	if spd == nil || spd.Annotations == nil {
		return ""
	}
	return spd.Annotations[consts.ServiceProfileDescriptorAnnotationKeyConfigHash]
}

// SetSPDHash set spd hash to spd annotation
func SetSPDHash(spd *apiworkload.ServiceProfileDescriptor, hash string) {
	if spd == nil {
		return
	}

	if spd.Annotations == nil {
		spd.Annotations = map[string]string{}
	}

	spd.Annotations[consts.ServiceProfileDescriptorAnnotationKeyConfigHash] = hash
}

// CalculateSPDHash calculate current spd hash by its spec and status
func CalculateSPDHash(spd *apiworkload.ServiceProfileDescriptor) (string, error) {
	if spd == nil {
		return "", fmt.Errorf("spd is nil")
	}

	spdCopy := &apiworkload.ServiceProfileDescriptor{}
	spdCopy.Annotations = make(map[string]string)

	if sentinel, ok := spd.Annotations[apiconsts.SPDAnnotationBaselineSentinelKey]; ok {
		spdCopy.Annotations[apiconsts.SPDAnnotationBaselineSentinelKey] = sentinel
	}

	if sentinel, ok := spd.Annotations[apiconsts.SPDAnnotationExtendedBaselineSentinelKey]; ok {
		spdCopy.Annotations[apiconsts.SPDAnnotationExtendedBaselineSentinelKey] = sentinel
	}

	spdCopy.Spec = spd.Spec
	spdCopy.Status = spd.Status
	data, err := json.Marshal(spdCopy)
	if err != nil {
		return "", err
	}

	return general.GenerateHash(data, spdConfigHashLength), nil
}

func SetLastFetchTime(spd *apiworkload.ServiceProfileDescriptor, t time.Time) {
	if spd == nil {
		return
	}

	if spd.Annotations == nil {
		spd.Annotations = map[string]string{}
	}

	spd.Annotations[consts.ServiceProfileDescriptorAnnotationKeyLastFetchTime] = t.Format(time.RFC3339)
}

func GetLastFetchTime(spd *apiworkload.ServiceProfileDescriptor) time.Time {
	if spd == nil {
		return time.Time{}
	}

	t, ok := spd.Annotations[consts.ServiceProfileDescriptorAnnotationKeyLastFetchTime]
	if !ok {
		return time.Time{}
	}

	lastFetchTime, err := time.Parse(time.RFC3339, t)
	if err != nil {
		klog.Errorf("get spd %v last fetch time failed, %v", native.GenerateUniqObjectNameKey(spd), err)
		return time.Time{}
	}

	return lastFetchTime
}

// GetPodSPDName gets spd name from pod annotation
func GetPodSPDName(podMeta metav1.ObjectMeta) (string, error) {
	spdName, ok := podMeta.GetAnnotations()[apiconsts.PodAnnotationSPDNameKey]
	if !ok {
		return "", fmt.Errorf("pod without spd annotation")
	}

	return spdName, nil
}

// GetExtendedIndicatorSpec get extended indicator spec by baseline percent and indicators.
// The indicators must be a pointer to a struct that has a suffix "Indicators" in its name
// and the indicators must be an implement of runtime.Object and use AddKnownTypes add to scheme
// with the same group and version as the spd
func GetExtendedIndicatorSpec(baselinePercent *int32, indicators interface{}) (*apiworkload.ServiceExtendedIndicatorSpec, error) {
	name, o, err := GetExtendedIndicator(indicators)
	if err != nil {
		return nil, err
	}

	return &apiworkload.ServiceExtendedIndicatorSpec{
		Name:            name,
		BaselinePercent: baselinePercent,
		Indicators: runtime.RawExtension{
			Object: o,
		},
	}, nil
}

// GetExtendedIndicator get extended indicator name and object
func GetExtendedIndicator(indicators interface{}) (string, runtime.Object, error) {
	if indicators == nil {
		return "", nil, fmt.Errorf("extended indicators is nil")
	}

	t := reflect.TypeOf(indicators)
	if t.Kind() != reflect.Ptr {
		return "", nil, fmt.Errorf("extended indicators must be pointers to structs")
	}

	o, ok := indicators.(runtime.Object)
	if !ok {
		return "", nil, fmt.Errorf("extended indicators must be an implement of runtime.Object")
	}

	name := t.Elem().Name()
	if !strings.HasSuffix(name, apiworkload.ExtendedIndicatorSuffix) {
		return "", nil, fmt.Errorf("extended indicators must have suffix 'Indicators'")
	}

	return strings.TrimSuffix(name, apiworkload.ExtendedIndicatorSuffix), o, nil
}

func GetSPDExtendedIndicators(spd *apiworkload.ServiceProfileDescriptor, indicators interface{}) (*int32, error) {
	name, o, err := GetExtendedIndicator(indicators)
	if err != nil {
		return nil, err
	}

	for _, indicator := range spd.Spec.ExtendedIndicator {
		if indicator.Name != name {
			continue
		}

		object := indicator.Indicators.Object
		raw := indicator.Indicators.Raw
		if object == nil && raw == nil {
			return nil, fmt.Errorf("%s indicators object is nil", name)
		}

		if object != nil {
			t := reflect.TypeOf(indicators)
			if t.Kind() != reflect.Ptr {
				return nil, fmt.Errorf("indicators must be pointers to structs")
			}

			v := reflect.ValueOf(object)
			if !v.CanConvert(t) {
				return nil, fmt.Errorf("%s indicators object cannot convert to %v", name, t.Name())
			}

			reflect.ValueOf(indicators).Elem().Set(v.Convert(t).Elem())
		} else {
			object, ok := indicators.(runtime.Object)
			if !ok {
				return nil, fmt.Errorf("%s indicators object cannot convert to runtime.Object", name)
			}

			deserializer := scheme.Codecs.UniversalDeserializer()
			_, _, err := deserializer.Decode(raw, nil, object)
			if err != nil {
				return nil, err
			}
		}

		return indicator.BaselinePercent, nil
	}
	return nil, apierrors.NewNotFound(schema.GroupResource{
		Group:    apiworkload.GroupName,
		Resource: strings.ToLower(o.GetObjectKind().GroupVersionKind().Kind),
	}, name)
}

func AggregateMetrics(metrics []resource.Quantity, aggregator apiworkload.Aggregator) (*resource.Quantity, error) {
	switch aggregator {
	case apiworkload.Avg:
		return native.AggregateAvgQuantities(metrics), nil
	case apiworkload.Max:
		return native.AggregateMaxQuantities(metrics), nil
	case apiworkload.Sum:
		return native.AggregateSumQuantities(metrics), nil
	default:
		return nil, fmt.Errorf("not support aggregator %v", aggregator)
	}
}
