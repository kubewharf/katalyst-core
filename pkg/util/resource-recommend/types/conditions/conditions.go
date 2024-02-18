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

package conditions

import (
	"sort"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
)

// ResourceRecommendConditionsMap is map from recommend condition type to condition.
type ResourceRecommendConditionsMap map[v1alpha1.ResourceRecommendConditionType]v1alpha1.ResourceRecommendCondition

func NewResourceRecommendConditionsMap() *ResourceRecommendConditionsMap {
	convertedConditionsMap := make(ResourceRecommendConditionsMap)
	return &convertedConditionsMap
}

func (conditionsMap *ResourceRecommendConditionsMap) Set(condition v1alpha1.ResourceRecommendCondition) {
	oldCondition, alreadyPresent := (*conditionsMap)[condition.Type]
	if alreadyPresent && oldCondition.Type == condition.Type &&
		oldCondition.Status == condition.Status &&
		oldCondition.Reason == condition.Reason &&
		oldCondition.Message == condition.Message {
		return
	} else {
		condition.LastTransitionTime = metav1.Now()
	}
	(*conditionsMap)[condition.Type] = condition
}

func (conditionsMap *ResourceRecommendConditionsMap) AsList() []v1alpha1.ResourceRecommendCondition {
	conditions := make([]v1alpha1.ResourceRecommendCondition, 0, len(*conditionsMap))
	for _, condition := range *conditionsMap {
		conditions = append(conditions, condition)
	}

	// Sort conditions by type to avoid elements floating on the list
	sort.Slice(conditions, func(i, j int) bool {
		return conditions[i].Type < conditions[j].Type
	})

	return conditions
}

func (conditionsMap *ResourceRecommendConditionsMap) ConditionActive(conditionType v1alpha1.ResourceRecommendConditionType) bool {
	condition, found := (*conditionsMap)[conditionType]
	return found && condition.Status == v1.ConditionTrue
}

func ValidationSucceededCondition() *v1alpha1.ResourceRecommendCondition {
	return &v1alpha1.ResourceRecommendCondition{
		Type:   v1alpha1.Validated,
		Status: v1.ConditionTrue,
	}
}

func InitializationSucceededCondition() *v1alpha1.ResourceRecommendCondition {
	return &v1alpha1.ResourceRecommendCondition{
		Type:   v1alpha1.Initialized,
		Status: v1.ConditionTrue,
	}
}

func RecommendationReadyCondition() *v1alpha1.ResourceRecommendCondition {
	return &v1alpha1.ResourceRecommendCondition{
		Type:   v1alpha1.RecommendationProvided,
		Status: v1.ConditionTrue,
	}
}

func ConvertCustomErrorToCondition(err errortypes.CustomError) *v1alpha1.ResourceRecommendCondition {
	var conditionType v1alpha1.ResourceRecommendConditionType
	switch err.Phase {
	case errortypes.Validated:
		conditionType = v1alpha1.Validated
	case errortypes.ProcessRegister:
		conditionType = v1alpha1.Initialized
	case errortypes.RecommendationProvided:
		conditionType = v1alpha1.RecommendationProvided
	default:
		conditionType = v1alpha1.ResourceRecommendConditionType(err.Phase)
	}

	return &v1alpha1.ResourceRecommendCondition{
		Type:    conditionType,
		Status:  v1.ConditionFalse,
		Reason:  string(err.Code),
		Message: err.Message,
	}
}
