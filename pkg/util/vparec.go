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
	"fmt"

	"github.com/pkg/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/tools/cache"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/scheme"
	autoscalelister "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

/*
 helper functions to get vpa-rec-related objects with indexed informer
*/

// GetVPARecForVPA is used to get vpaRec that should be managed the given vpa
func getVPARecFroVPAWithIndex(vpa *apis.KatalystVerticalPodAutoscaler, vpaRecIndexer cache.Indexer) (*apis.VerticalPodAutoscalerRecommendation, error) {
	gvk, err := apiutil.GVKForObject(vpa, scheme.Scheme)
	if err != nil {
		return nil, err
	}
	ownerRef := metav1.OwnerReference{
		Name:       vpa.GetName(),
		Kind:       gvk.Kind,
		APIVersion: gvk.GroupVersion().String(),
		UID:        vpa.GetUID(),
	}

	objs, err := vpaRecIndexer.ByIndex(consts.OwnerReferenceIndex, native.GenerateObjectOwnerReferenceKey(ownerRef))
	if err != nil {
		return nil, errors.Wrapf(err, "vpaRec for vpa %s not exist", vpa.Name)
	} else if len(objs) > 1 || len(objs) == 0 {
		return nil, fmt.Errorf("vpaRec for vpa %s invalid", vpa.Name)
	}

	vpaRec, ok := objs[0].(*apis.VerticalPodAutoscalerRecommendation)
	if !ok {
		return nil, fmt.Errorf("invalid vpaRec")
	}
	return vpaRec, nil
}

/*
 helper functions to build indexed informer for vpa-rec-related objects
*/

// GetVPAForVPARec is used to get vpa that should manage the given vpaRec
func GetVPAForVPARec(vpaRec *apis.VerticalPodAutoscalerRecommendation,
	vpaLister autoscalelister.KatalystVerticalPodAutoscalerLister,
) (*apis.KatalystVerticalPodAutoscaler, error) {
	ownerReferences := vpaRec.GetOwnerReferences()
	if len(ownerReferences) == 0 {
		return nil, fmt.Errorf(" vpaRec %v has no owner", vpaRec.Name)
	} else if len(ownerReferences) > 1 {
		return nil, fmt.Errorf(" vpaRec %v has more than one owner", vpaRec.Name)
	}

	vpa, err := vpaLister.KatalystVerticalPodAutoscalers(vpaRec.Namespace).Get(ownerReferences[0].Name)
	if err != nil {
		return nil, fmt.Errorf("failed to get vpa %v : %v", ownerReferences[0].Name, err)
	}

	if !CheckVPARecommendationMatchVPA(vpaRec, vpa) {
		return nil, fmt.Errorf("VPA UID mismatch, VPA %s may be deleted", vpa.Name)
	}

	return vpa, nil
}

// GetVPARecForVPA is used to get vpaRec that should be managed the given vpa
func GetVPARecForVPA(vpa *apis.KatalystVerticalPodAutoscaler, vpaRecIndexer cache.Indexer,
	recLister autoscalelister.VerticalPodAutoscalerRecommendationLister,
) (*apis.VerticalPodAutoscalerRecommendation, error) {
	if recName, ok := vpa.GetAnnotations()[apiconsts.VPAAnnotationVPARecNameKey]; ok {
		vpaRec, err := recLister.VerticalPodAutoscalerRecommendations(vpa.Namespace).Get(recName)
		if err == nil && CheckVPARecommendationMatchVPA(vpaRec, vpa) {
			return vpaRec, nil
		}
	}

	if vpaRecIndexer != nil {
		if vpaRec, err := getVPARecFroVPAWithIndex(vpa, vpaRecIndexer); err == nil {
			return vpaRec, nil
		}
	}

	recList, err := recLister.List(labels.Everything())
	if err != nil {
		return nil, err
	}

	for _, vpaRec := range recList {
		if CheckVPARecommendationMatchVPA(vpaRec, vpa) {
			return vpaRec, nil
		}
	}
	return nil, apierrors.NewNotFound(apis.Resource(apis.ResourceNameVPARecommendation), "vparec for vpa")
}
