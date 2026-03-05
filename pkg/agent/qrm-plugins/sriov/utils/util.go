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

package utils

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/sriov/state"
)

func UpdateSriovVFResultAnnotation(client kubernetes.Interface, allocationInfo *state.AllocationInfo) error {
	annotationPatch := fmt.Sprintf(`{"metadata":{"annotations":{"%s":"%s"}}}`,
		apiconsts.PodAnnotationSriovVFResultKey, allocationInfo.VFInfo.RepName)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		_, err := client.CoreV1().Pods(allocationInfo.PodNamespace).Patch(context.Background(),
			allocationInfo.PodName, types.MergePatchType, []byte(annotationPatch), metav1.PatchOptions{})
		return err
	})

	return err
}
