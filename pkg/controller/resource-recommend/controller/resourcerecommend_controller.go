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

package controller

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	"golang.org/x/time/rate"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/kubewharf/katalyst-api/pkg/apis/recommendation/v1alpha1"
	processormanager "github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/processor/manager"
	recommendermanager "github.com/kubewharf/katalyst-core/pkg/controller/resource-recommend/recommender/manager"
	resourceutils "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/resource"
	conditionstypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/conditions"
	errortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/error"
	processortypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/processor"
	recommendationtypes "github.com/kubewharf/katalyst-core/pkg/util/resource-recommend/types/recommendation"
)

const (
	ExponentialFailureRateLimiterBaseDelay = time.Minute
	ExponentialFailureRateLimiterMaxDelay  = 30 * time.Minute
	DefaultRecommendInterval               = 24 * time.Hour
)

// ResourceRecommendController reconciles a ResourceRecommend object
type ResourceRecommendController struct {
	client.Client
	Scheme             *runtime.Scheme
	ProcessorManager   *processormanager.Manager
	RecommenderManager *recommendermanager.Manager
}

//+kubebuilder:rbac:groups=recommendation.katalyst.kubewharf.io,resources=resourcerecommends,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=recommendation.katalyst.kubewharf.io,resources=resourcerecommends/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=recommendation.katalyst.kubewharf.io,resources=resourcerecommends/finalizers,verbs=update

// SetupWithManager sets up the controller with the Manager.
func (r *ResourceRecommendController) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&v1alpha1.ResourceRecommend{}).
		// We will only focus on the event of the Spec update, filter update events for status and meta
		WithEventFilter(predicate.GenerationChangedPredicate{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: 10,
			RecoverPanic:            true,
			RateLimiter: workqueue.NewMaxOfRateLimiter(
				// For reconcile failures(i.e. reconcile return err), the retry time is (2*minutes)*2^<num-failures>
				// The maximum retry time is 24 hours
				workqueue.NewItemExponentialFailureRateLimiter(ExponentialFailureRateLimiterBaseDelay, ExponentialFailureRateLimiterMaxDelay),
				// 10 qps, 100 bucket size. This is only for retry speed and its only the overall factor (not per item)
				&workqueue.BucketRateLimiter{Limiter: rate.NewLimiter(rate.Limit(10), 100)},
			),
		}).
		Complete(r)
}

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// compare the state specified by the ResourceRecommend object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.12.2/pkg/reconcile
func (r *ResourceRecommendController) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	klog.InfoS("Get resourceRecommend to reconcile", "req", req)
	resourceRecommend := &v1alpha1.ResourceRecommend{}
	err := r.Get(ctx, req.NamespacedName, resourceRecommend)
	if err != nil {
		if k8serrors.IsNotFound(err) {
			// Object not found, return
			klog.V(2).InfoS("ResourceRecommend has been deleted.", "req", req)
			// CancelTasks err dno‘t need to be processed, because the Processor side has gc logic
			_ = r.CancelTasks(req.NamespacedName)
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	if recommender := resourceRecommend.Spec.ResourcePolicy.AlgorithmPolicy.Recommender; recommender != "" &&
		recommender != recommendationtypes.DefaultRecommenderType {
		klog.InfoS("ResourceRecommend is not controlled by the default controller", "req", req)
		return ctrl.Result{}, nil
	}

	if lastRecommendationTime := resourceRecommend.Status.LastRecommendationTime; lastRecommendationTime != nil {
		requeueAfter := time.Until(lastRecommendationTime.Add(DefaultRecommendInterval))
		observedGeneration := resourceRecommend.Status.ObservedGeneration
		if requeueAfter > time.Duration(0) && observedGeneration == resourceRecommend.GetGeneration() {
			klog.InfoS("no spec change and not time to reconcile, skipping this reconcile", "requeueAfter", requeueAfter, "observedGeneration", observedGeneration, "generation", resourceRecommend.GetGeneration(), "resourceRecommendName", resourceRecommend.GetName())
			return ctrl.Result{RequeueAfter: requeueAfter}, nil
		}
	}

	err = r.doReconcile(ctx, req.NamespacedName, resourceRecommend)
	if err != nil {
		klog.ErrorS(err, "Failed to reconcile", "req", req)
		return ctrl.Result{}, err
	}

	klog.InfoS("reconcile succeeded, requeue after "+DefaultRecommendInterval.String(), "req", req)
	// reconcile succeeded, requeue after 24hours
	return ctrl.Result{RequeueAfter: DefaultRecommendInterval}, nil
}

func (r *ResourceRecommendController) doReconcile(ctx context.Context, namespacedName k8stypes.NamespacedName,
	resourceRecommend *v1alpha1.ResourceRecommend,
) (err error) {
	recommendation := recommendationtypes.NewRecommendation(resourceRecommend)
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
			klog.ErrorS(err, "doReconcile panic", "resourceRecommend", namespacedName, "stack", string(debug.Stack()))
			return
		}
		updateStatusErr := r.UpdateRecommendationStatus(namespacedName, recommendation)
		if err == nil {
			err = updateStatusErr
		}
	}()

	// conduct validation
	validationError := recommendation.SetConfig(ctx, r.Client, resourceRecommend)
	if validationError != nil {
		klog.ErrorS(validationError, "Failed to get Recommendation", "resourceRecommend", namespacedName)
		recommendation.Conditions.Set(*conditionstypes.ConvertCustomErrorToCondition(*validationError))
		return validationError
	}
	// set the condition of the validation step to be true
	recommendation.Conditions.Set(*conditionstypes.ValidationSucceededCondition())

	// Initialization
	if registerTaskErr := r.RegisterTasks(*recommendation); registerTaskErr != nil {
		klog.ErrorS(registerTaskErr, "Failed to register process task", "resourceRecommend", namespacedName)
		recommendation.Conditions.Set(*conditionstypes.ConvertCustomErrorToCondition(*registerTaskErr))
		return registerTaskErr
	}
	// set the condition of the initialization step to be true
	recommendation.Conditions.Set(*conditionstypes.InitializationSucceededCondition())

	// recommendation logic
	defaultRecommender := r.RecommenderManager.NewRecommender(recommendation.AlgorithmPolicy.Algorithm)
	if recommendationError := defaultRecommender.Recommend(recommendation); recommendationError != nil {
		klog.ErrorS(recommendationError, "error when getting recommendation for resource", "resourceRecommend", namespacedName)
		recommendation.Conditions.Set(*conditionstypes.ConvertCustomErrorToCondition(*recommendationError))
		return recommendationError
	}
	// set the condition of the recommendation step to be true
	recommendation.Conditions.Set(*conditionstypes.RecommendationReadyCondition())
	return nil
}

// RegisterTasks Register all process task
func (r *ResourceRecommendController) RegisterTasks(recommendation recommendationtypes.Recommendation) *errortypes.CustomError {
	processor := r.ProcessorManager.GetProcessor(recommendation.AlgorithmPolicy.Algorithm)
	for _, container := range recommendation.Containers {
		for _, containerConfig := range container.ContainerConfigs {
			processConfig := processortypes.NewProcessConfig(recommendation.NamespacedName, recommendation.Config.TargetRef, container.ContainerName, containerConfig.ControlledResource, "")
			if err := processor.Register(processConfig); err != nil {
				return errortypes.DataProcessRegisteredFailedError(err.Error())
			}
		}
	}

	return nil
}

// CancelTasks Cancel all process task
func (r *ResourceRecommendController) CancelTasks(namespacedName k8stypes.NamespacedName) *errortypes.CustomError {
	processor := r.ProcessorManager.GetProcessor(v1alpha1.AlgorithmPercentile)
	err := processor.Cancel(&processortypes.ProcessKey{ResourceRecommendNamespacedName: namespacedName})
	if err != nil {
		klog.ErrorS(err, "cancel processor task failed", "namespacedName", namespacedName)
	}
	return err
}

func (r *ResourceRecommendController) UpdateRecommendationStatus(namespaceName k8stypes.NamespacedName,
	recommendation *recommendationtypes.Recommendation,
) error {
	updateStatus := &v1alpha1.ResourceRecommend{
		Status: recommendation.AsStatus(),
	}
	// record generation
	updateStatus.Status.ObservedGeneration = recommendation.ObservedGeneration

	err := resourceutils.PatchUpdateResourceRecommend(r.Client, namespaceName, updateStatus)
	if err != nil {
		klog.ErrorS(err, "Update resourceRecommend status error")
	}
	return err
}
