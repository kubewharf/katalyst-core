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

package resource

import (
	"context"
	"encoding/json"
	"fmt"
	v1 "k8s.io/api/apps/v1"
	appsv1 "k8s.io/client-go/kubernetes/typed/apps/v1"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	k8sclient "sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
)

func ConvertAndGetResource(ctx context.Context, client appsv1.AppsV1Interface, namespace string, targetRef v1alpha1.CrossVersionObjectReference) (*v1.Deployment, error) {
	klog.V(5).Infof("get resource in targetRef: %v, namespace: %v", targetRef, namespace)
	deployment, err := client.Deployments(namespace).Get(ctx, targetRef.Name, metav1.GetOptions{
		TypeMeta: metav1.TypeMeta{
			Kind:       targetRef.Kind,
			APIVersion: targetRef.APIVersion,
		},
	})
	if err != nil {
		return nil, err
	}
	return deployment, nil
}

func GetAllClaimedContainers(deployment *v1.Deployment) ([]string, error) {
	if deployment == nil {
		return nil, fmt.Errorf("get containers failed, deployment is nil object")
	}
	containerName := make([]string, 0, len(deployment.Spec.Template.Spec.Containers))
	for _, v := range deployment.Spec.Template.Spec.Containers {
		containerName = append(containerName, v.Name)
	}
	return containerName, nil
}

type patchRecord struct {
	Op    string      `json:"op,inline"`
	Path  string      `json:"path,inline"`
	Value interface{} `json:"value"`
}

func PatchUpdateResourceRecommend(client k8sclient.Client, namespaceName k8stypes.NamespacedName,
	resourceRecommend *v1alpha1.ResourceRecommend,
) error {
	obj := &v1alpha1.ResourceRecommend{
		ObjectMeta: metav1.ObjectMeta{
			Name:      namespaceName.Name,
			Namespace: namespaceName.Namespace,
		},
	}
	patches := []patchRecord{{
		Op:    "replace",
		Path:  "/status",
		Value: resourceRecommend.Status,
	}}

	patch, err := json.Marshal(patches)
	if err != nil {
		return errors.Wrapf(err, "failed to Marshal resourceRecommend: %+v", resourceRecommend)
	}
	patchDate := k8sclient.RawPatch(k8stypes.JSONPatchType, patch)
	err = client.Status().Patch(context.TODO(), obj, patchDate)
	if err != nil {
		return errors.Wrapf(err, "failed to patch resource")
	}
	return nil
}
