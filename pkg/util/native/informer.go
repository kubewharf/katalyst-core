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

package native

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/informers"
)

// WorkloadInformer keeps the informer-related contents for each workload
type WorkloadInformer struct {
	GVK      *schema.GroupVersionKind
	GVR      *schema.GroupVersionResource
	Informer informers.GenericInformer
}

// MakeWorkloadInformers generate informers for workloads dynamically with rest-mapper
func MakeWorkloadInformers(
	workLoads []string,
	mapper meta.RESTMapper,
	dynamicInformerFactory dynamicinformer.DynamicSharedInformerFactory,
) (map[string]WorkloadInformer, error) {
	workloadInformers := map[string]WorkloadInformer{}
	for _, workload := range workLoads {
		gvr, _ := schema.ParseResourceArg(workload)
		if gvr == nil {
			return nil, fmt.Errorf("ParseResourceArg worload %v failed", workload)
		}
		workloadInformer := dynamicInformerFactory.ForResource(*gvr)

		gvk, err := mapper.KindFor(*gvr)
		if err != nil {
			return nil, fmt.Errorf("find for %v failed, err %v", gvr.String(), err)
		}
		workloadInformers[workload] = WorkloadInformer{
			GVK:      &gvk,
			GVR:      gvr,
			Informer: workloadInformer,
		}
	}

	return workloadInformers, nil
}
