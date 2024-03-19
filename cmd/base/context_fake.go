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

package katalyst_base

import (
	"fmt"
	"reflect"
	"strconv"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	fakedisco "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	metaFake "k8s.io/client-go/metadata/fake"
	coretesting "k8s.io/client-go/testing"
	"k8s.io/kube-aggregator/pkg/apis/apiregistration"
	aggregatorfake "k8s.io/kube-aggregator/pkg/client/clientset_generated/clientset/fake"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	overcommitapis "github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	externalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/util/credential"
	"github.com/kubewharf/katalyst-core/pkg/util/credential/authorization"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

func nilObjectFilter(object []runtime.Object) []runtime.Object {
	objects := make([]runtime.Object, 0)
	for _, o := range object {
		if reflect.ValueOf(o).IsNil() {
			continue
		}
		objects = append(objects, o)
	}
	return objects
}

var fakeDiscoveryClient = &fakedisco.FakeDiscovery{Fake: &coretesting.Fake{
	Resources: []*metav1.APIResourceList{
		{
			GroupVersion: appsv1.SchemeGroupVersion.String(),
			APIResources: []metav1.APIResource{
				{Name: "deployments", Namespaced: true, Kind: "Deployment"},
				{Name: "replicasets", Namespaced: true, Kind: "Replica"},
				{Name: "statefulsets", Namespaced: true, Kind: "StatefulSet"},
			},
		},
		{
			GroupVersion: v1.SchemeGroupVersion.String(),
			APIResources: []metav1.APIResource{
				{Name: "pods", Namespaced: true, Kind: "Pod"},
			},
		},
		{
			GroupVersion: v1alpha1.SchemeGroupVersion.String(),
			APIResources: []metav1.APIResource{
				{Name: v1alpha1.ResourceNameAdminQoSConfigurations, Namespaced: true, Kind: crd.ResourceKindAdminQoSConfiguration},
				{Name: v1alpha1.ResourceNameAuthConfigurations, Namespaced: true, Kind: crd.ResourceKindAuthConfiguration},
			},
		},
	},
}}

// versionedUpdate increases object resource version if needed;
// and if the resourceVersion of is not equal to origin one, returns a conflict error
func versionedUpdate(tracker coretesting.ObjectTracker, gvr schema.GroupVersionResource,
	obj runtime.Object, ns string) error {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return fmt.Errorf("failed to get accessor for object: %v", err)
	}

	if accessor.GetName() == "" {
		return apierrors.NewInvalid(
			obj.GetObjectKind().GroupVersionKind().GroupKind(),
			accessor.GetName(),
			field.ErrorList{field.Required(field.NewPath("metadata.name"), "name is required")})
	}

	oldObject, err := tracker.Get(gvr, ns, accessor.GetName())
	if err != nil {
		return err
	}

	oldAccessor, err := meta.Accessor(oldObject)
	if err != nil {
		return err
	}

	// if the new object does not have the resource version set, and it allows unconditional update,
	// default it to the resource version of the existing resource
	if accessor.GetResourceVersion() == "" {
		accessor.SetResourceVersion(oldAccessor.GetResourceVersion())
	}
	if accessor.GetResourceVersion() != oldAccessor.GetResourceVersion() {
		return apierrors.NewConflict(gvr.GroupResource(), accessor.GetName(), errors.New("object was modified"))
	}

	return increaseObjectResourceVersion(tracker, gvr, obj, ns)
}

// increaseObjectResourceVersion increase object resourceVersion if needed, and if the resourceVersion is empty just skip it
func increaseObjectResourceVersion(tracker coretesting.ObjectTracker, gvr schema.GroupVersionResource,
	obj runtime.Object, ns string) error {
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return fmt.Errorf("failed to get accessor for object: %v", err)
	}

	if accessor.GetResourceVersion() == "" {
		accessor.SetResourceVersion("0")
	}

	intResourceVersion, err := strconv.ParseUint(accessor.GetResourceVersion(), 10, 64)
	if err != nil {
		return fmt.Errorf("can not convert resourceVersion %q to int: %v", accessor.GetResourceVersion(), err)
	}
	intResourceVersion++
	accessor.SetResourceVersion(strconv.FormatUint(intResourceVersion, 10))

	return tracker.Update(gvr, obj, ns)
}

// versionedUpdateReactor is reactor for versioned update
func versionedUpdateReactor(tracker coretesting.ObjectTracker,
	action coretesting.UpdateActionImpl) (bool, runtime.Object, error) {
	obj := action.GetObject()
	accessor, err := meta.Accessor(obj)
	if err != nil {
		return true, nil, err
	}

	// if resource version is empty just fallback to default logic
	if accessor.GetResourceVersion() == "" {
		return false, nil, nil
	}

	err = versionedUpdate(tracker, action.GetResource(), obj, action.GetNamespace())
	if err != nil {
		return true, nil, err
	}

	obj, err = tracker.Get(action.GetResource(), action.GetNamespace(), accessor.GetName())
	return true, obj, err
}

// versionedPatchReactor is reactor for versioned patch
func versionedPatchReactor(tracker coretesting.ObjectTracker,
	action coretesting.PatchActionImpl) (bool, runtime.Object, error) {
	obj, err := tracker.Get(action.GetResource(), action.GetNamespace(), action.GetName())
	if err != nil {
		return true, nil, err
	}

	accessor, err := meta.Accessor(obj)
	if err != nil {
		return true, nil, err
	}

	// if resource version is empty just fallback to default logic
	if accessor.GetResourceVersion() == "" {
		return false, nil, nil
	}

	// increases object resourceVersion even if no field update happen
	err = increaseObjectResourceVersion(tracker, action.GetResource(), obj, action.GetNamespace())
	if err != nil {
		return true, nil, err
	}

	// return false to continue the next real patch reactor
	return false, nil, nil
}

// prependVersionedUpdateAndPatchReactor adds a ResourceVersion change reactor to support
// fake client CAS update or patch an object if its ResourceVersion already set
func prependVersionedUpdateAndPatchReactor(fakeClient coretesting.FakeClient) {
	fakeClient.PrependReactor("*", "*", func(action coretesting.Action) (handled bool, ret runtime.Object, err error) {
		tracker := fakeClient.Tracker()
		switch action := action.(type) {
		case coretesting.UpdateActionImpl:
			return versionedUpdateReactor(tracker, action)
		case coretesting.PatchActionImpl:
			return versionedPatchReactor(tracker, action)
		default:
			return false, nil, fmt.Errorf("no reaction implemented for %s", action)
		}
	})
}

func GenerateFakeGenericContext(objects ...[]runtime.Object) (*GenericContext, error) {
	var kubeObjects, internalObjects, dynamicObjects, metaObjects []runtime.Object
	if len(objects) > 0 {
		kubeObjects = objects[0]
	}
	if len(objects) > 1 {
		internalObjects = objects[1]
	}
	if len(objects) > 2 {
		dynamicObjects = objects[2]
	}
	if len(objects) > 3 {
		metaObjects = objects[3]
	}

	scheme := runtime.NewScheme()
	utilruntime.Must(metav1.AddMetaToScheme(scheme))
	utilruntime.Must(v1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))
	utilruntime.Must(apis.AddToScheme(scheme))
	utilruntime.Must(workloadapis.AddToScheme(scheme))
	utilruntime.Must(v1alpha1.AddToScheme(scheme))
	utilruntime.Must(overcommitapis.AddToScheme(scheme))
	utilruntime.Must(apiregistration.AddToScheme(scheme))
	utilruntime.Must(nodev1alpha1.AddToScheme(scheme))

	fakeMetaClient := metaFake.NewSimpleMetadataClient(scheme, nilObjectFilter(metaObjects)...)
	fakeInternalClient := externalfake.NewSimpleClientset(nilObjectFilter(internalObjects)...)
	fakeDynamicClient := dynamicfake.NewSimpleDynamicClient(scheme, nilObjectFilter(dynamicObjects)...)
	fakeKubeClient := fake.NewSimpleClientset(nilObjectFilter(kubeObjects)...)
	fakeAggregatorClient := aggregatorfake.NewSimpleClientset()

	prependVersionedUpdateAndPatchReactor(fakeKubeClient)
	prependVersionedUpdateAndPatchReactor(fakeInternalClient)
	prependVersionedUpdateAndPatchReactor(fakeDynamicClient)

	clientSet := client.GenericClientSet{
		MetaClient:       fakeMetaClient,
		KubeClient:       fakeKubeClient,
		InternalClient:   fakeInternalClient,
		DynamicClient:    fakeDynamicClient,
		DiscoveryClient:  fakeDiscoveryClient,
		AggregatorClient: fakeAggregatorClient,
	}

	var dynamicResources []string
	for _, o := range nilObjectFilter(dynamicObjects) {
		gvr, _ := meta.UnsafeGuessKindToResource(o.GetObjectKind().GroupVersionKind())
		dynamicResources = append(dynamicResources, native.GenerateDynamicResourceByGVR(gvr))
	}

	genericConf := &generic.GenericConfiguration{
		AuthConfiguration: &generic.AuthConfiguration{
			AuthType:          credential.AuthTypeInsecure,
			AccessControlType: authorization.AccessControlTypeInsecure,
		},
	}
	controlCtx, err := NewGenericContext(&clientSet, "", dynamicResources,
		sets.NewString(), genericConf, "", nil)
	return controlCtx, err
}
