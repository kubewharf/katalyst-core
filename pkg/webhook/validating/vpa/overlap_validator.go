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

package vpa

import (
	"fmt"

	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/klog/v2"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	apiListers "github.com/kubewharf/katalyst-api/pkg/client/listers/autoscaling/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

// WebhookVPAOverlapValidator validate if pod for one vpa overlap with other pods by checking their target reference
type WebhookVPAOverlapValidator struct {
	// vpaLister can list/get VerticalPodAutoscaler from the shared informer's store
	vpaLister apiListers.KatalystVerticalPodAutoscalerLister
}

func NewWebhookVPAOverlapValidator(vpaLister apiListers.KatalystVerticalPodAutoscalerLister) *WebhookVPAOverlapValidator {
	return &WebhookVPAOverlapValidator{
		vpaLister: vpaLister,
	}
}

func (wo *WebhookVPAOverlapValidator) ValidateVPA(vpa *apis.KatalystVerticalPodAutoscaler) (valid bool, message string, err error) {
	if vpa == nil {
		err := fmt.Errorf("vpa is nil")
		return false, err.Error(), err
	}

	// todo: add cache here to avoid list all vpa
	vpas, err := wo.vpaLister.List(labels.Everything())
	if err != nil {
		return false, "failed to list all vpas", err
	}
	klog.V(5).Infof("find %d vpa existing", len(vpas))

	for _, anotherVPA := range vpas {
		if anotherVPA == nil {
			err := fmt.Errorf("vpa can not be nil")
			return false, err.Error(), err
		}
		if native.CheckObjectEqual(vpa, anotherVPA) {
			klog.Infof("ignore same vpa (%s/%s/%s)", anotherVPA.Namespace, anotherVPA.Name, anotherVPA.UID)
			continue
		}
		if apiequality.Semantic.DeepEqual(vpa.Spec.TargetRef, anotherVPA.Spec.TargetRef) {
			klog.Info("different vpa have same target reference")
			return false, "different vpa have same target reference", nil
		}
	}

	return true, "", nil
}
