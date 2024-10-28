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

package recommendation

import (
	"context"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/restmapper"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	conditionstypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/conditions"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
)

const (
	// TargetRefKindDeployment is Deployment
	TargetRefKindDeployment string = "Deployment"
)

var TargetRefKinds = []string{TargetRefKindDeployment}

const (
	DefaultRecommenderType = "default"
)

const (
	PercentileAlgorithmType = "percentile"
	// DefaultAlgorithmType use percentile as the default algorithm
	DefaultAlgorithmType = PercentileAlgorithmType
)

var AlgorithmTypes = []string{PercentileAlgorithmType}

var ResourceNames = []v1.ResourceName{v1.ResourceCPU, v1.ResourceMemory}

var SupportControlledValues = []v1alpha1.ContainerControlledValues{v1alpha1.ContainerControlledValuesRequestsOnly}

const (
	DefaultUsageBuffer = 30
	MaxUsageBuffer     = 100
	MinUsageBuffer     = 0
)

const (
	ContainerPolicySelectAllFlag = "*"
)

type Recommendation struct {
	types.NamespacedName
	ObservedGeneration int64
	Config
	// Map of the status conditions (keys are condition types).
	Conditions      *conditionstypes.ResourceRecommendConditionsMap
	Recommendations []v1alpha1.ContainerResources
}

type Config struct {
	TargetRef       v1alpha1.CrossVersionObjectReference
	Containers      []Container
	AlgorithmPolicy v1alpha1.AlgorithmPolicy
}

type Container struct {
	ContainerName    string
	ContainerConfigs []ContainerConfig
}

type ContainerConfig struct {
	ControlledResource    v1.ResourceName
	ResourceBufferPercent int32
}

func NewRecommendation(resourceRecommend *v1alpha1.ResourceRecommend) *Recommendation {
	return &Recommendation{
		NamespacedName:     types.NamespacedName{Name: resourceRecommend.Name, Namespace: resourceRecommend.Namespace},
		ObservedGeneration: resourceRecommend.Generation,
		Conditions:         conditionstypes.NewResourceRecommendConditionsMap(),
	}
}

func (r *Recommendation) SetConfig(ctx context.Context, client dynamic.Interface,
	resourceRecommend *v1alpha1.ResourceRecommend, mapper *restmapper.DeferredDiscoveryRESTMapper,
) *errortypes.CustomError {
	targetRef, customErr := ValidateAndExtractTargetRef(resourceRecommend.Spec.TargetRef)
	if customErr != nil {
		klog.Errorf("spec.targetRef validate error, "+
			"reason: %s, msg: %s", customErr.Code, customErr.Message)
		return customErr
	}

	algorithmPolicy, customErr := ValidateAndExtractAlgorithmPolicy(resourceRecommend.Spec.ResourcePolicy.AlgorithmPolicy)
	if customErr != nil {
		klog.Errorf("spec.resourcePolicy.algorithmPolicy validate error,"+
			" reason: %s, msg: %s", customErr.Code, customErr.Message)
		return customErr
	}

	containers, customErr := ValidateAndExtractContainers(ctx, client, resourceRecommend.Namespace,
		targetRef, resourceRecommend.Spec.ResourcePolicy.ContainerPolicies, mapper)
	if customErr != nil {
		klog.Errorf("spec.resourcePolicy.containerPolicies validate error, "+
			"reason: %s, msg: %s", customErr.Code, customErr.Message)
		return customErr
	}

	r.Config = Config{
		TargetRef:       targetRef,
		AlgorithmPolicy: algorithmPolicy,
		Containers:      containers,
	}
	return nil
}

// AsStatus returns this objects equivalent of VPA Status.
func (r *Recommendation) AsStatus() v1alpha1.ResourceRecommendStatus {
	status := v1alpha1.ResourceRecommendStatus{
		Conditions: r.Conditions.AsList(),
	}
	if r.Recommendations != nil {
		now := metav1.Now()
		status.LastRecommendationTime = &now
		status.RecommendResources = &v1alpha1.RecommendResources{
			ContainerRecommendations: r.Recommendations,
		}
	}
	return status
}
