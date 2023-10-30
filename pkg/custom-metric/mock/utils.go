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

package mock

import (
	"strconv"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func GenerateMockPods(namespaceNum, workloadNum, podNumPerWorkload int) []*v1.Pod {
	result := make([]*v1.Pod, 0, namespaceNum*workloadNum*podNumPerWorkload)
	namespaceGenerateName := "ns-"
	workloadGenerateName := "workload-"
	for namespaceIndex := 0; namespaceIndex < namespaceNum; namespaceIndex++ {
		namespace := namespaceGenerateName + strconv.Itoa(namespaceIndex)
		for workloadIndex := 0; workloadIndex < workloadNum; workloadIndex++ {
			workloadName := workloadGenerateName + strconv.Itoa(workloadIndex)
			for podIndex := 0; podIndex < podNumPerWorkload; podIndex++ {
				podName := workloadName + "-" + strconv.Itoa(podIndex)
				result = append(result, &v1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Namespace: namespace,
						Name:      podName,
						UID:       types.UID(podName),
						Labels: map[string]string{
							"app": workloadName,
						},
					},
				})
			}
		}
	}

	return result
}
