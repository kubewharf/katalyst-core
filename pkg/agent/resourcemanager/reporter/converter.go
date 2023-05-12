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

package reporter

import (
	"context"
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// Converter converts the report fields to the format the reporter expects, for example,
// if the target crd version needs to be changed from v1alpha1 to v1, the converter can convert
// the v1alpha1's plugin report fields to v1 fields and report to the v1 reporter
type Converter interface {
	Convert(ctx context.Context, reportFields []*v1alpha1.ReportField) (*v1alpha1.ReportContent, error)
}

var converterInitializers sync.Map

// ConverterInitFunc is the function to initialize a converter
type ConverterInitFunc func(*client.GenericClientSet, *metaserver.MetaServer,
	metrics.MetricEmitter, *config.Configuration) (Converter, error)

// RegisterConverterInitializer registers a converter initializer function
// for a specific gvk, the function will be called when the reporter manager
// is created. The function should return a converter for the gvk. If the
// function is called multiple times for the same gvk, the last one will be
// used.
func RegisterConverterInitializer(gvk metav1.GroupVersionKind, initFunc ConverterInitFunc) {
	converterInitializers.Store(gvk, initFunc)
}

func getRegisteredConverterInitializers() map[metav1.GroupVersionKind]ConverterInitFunc {
	converters := make(map[metav1.GroupVersionKind]ConverterInitFunc)
	converterInitializers.Range(func(key, value interface{}) bool {
		converters[key.(metav1.GroupVersionKind)] = value.(ConverterInitFunc)
		return true
	})
	return converters
}
