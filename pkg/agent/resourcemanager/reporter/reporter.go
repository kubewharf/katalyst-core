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

// Package reporter is responsible for collecting per-node
// resources for scheduler; those resources are collected through multiple
// different sources, and updated in different K8S objects for needs.
package reporter // import "github.com/kubewharf/katalyst-core/pkg/reportermanager/reporter"

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

// Reporter use to update specific kind crd
type Reporter interface {
	// Update receives ReportField list from report manager, the reporter implementation
	// should be responsible for assembling and updating the specific object
	// todo: consider whether we should perform real update actions asynchronously
	Update(ctx context.Context, fields []*v1alpha1.ReportField) error

	// Run starts the syncing logic of reporter
	Run(ctx context.Context)
}

var updaterInitializers sync.Map

// InitFunc is used to initialize a particular reporter.
type InitFunc func(*client.GenericClientSet, *metaserver.MetaServer, metrics.MetricEmitter, *config.Configuration) (Reporter, error)

func RegisterReporterInitializer(gvk metav1.GroupVersionKind, initFunc InitFunc) {
	updaterInitializers.Store(gvk, initFunc)
}

func GetRegisteredInitializers() map[metav1.GroupVersionKind]InitFunc {
	updaters := make(map[metav1.GroupVersionKind]InitFunc)
	updaterInitializers.Range(func(key, value interface{}) bool {
		updaters[key.(metav1.GroupVersionKind)] = value.(InitFunc)
		return true
	})
	return updaters
}
