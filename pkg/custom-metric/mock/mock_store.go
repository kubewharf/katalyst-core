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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/metadata/fake"
	"k8s.io/client-go/metadata/metadatainformer"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config"
)

const MetricStoreNameMockLocalMemory = "mock-local-memory-store"

func ReplaceMetaInformerWithMockData(baseCtx *katalystbase.GenericContext, conf *config.Configuration) {
	scheme := runtime.NewScheme()
	utilruntime.Must(metav1.AddMetaToScheme(scheme))
	utilruntime.Must(v1.AddToScheme(scheme))
	utilruntime.Must(appsv1.AddToScheme(scheme))

	pods := GenerateMockPods(conf.MockConfiguration.NamespaceCount, conf.MockConfiguration.WorkloadCount,
		conf.MockConfiguration.PodCount)
	objects := make([]runtime.Object, 0, len(pods))
	for i := range pods {
		objects = append(objects, &metav1.PartialObjectMetadata{
			TypeMeta: metav1.TypeMeta{
				APIVersion: "v1",
				Kind:       "Pod",
			},
			ObjectMeta: metav1.ObjectMeta{
				Namespace: pods[i].Namespace,
				Name:      pods[i].Name,
				Labels:    pods[i].Labels,
				UID:       pods[i].UID,
			},
		})
	}

	mockMetadataClient := fake.NewSimpleMetadataClient(scheme, objects...)
	baseCtx.MetaInformerFactory = metadatainformer.NewFilteredSharedInformerFactory(mockMetadataClient, time.Hour*24, "",
		func(options *metav1.ListOptions) {
			options.LabelSelector = ""
		})
}
