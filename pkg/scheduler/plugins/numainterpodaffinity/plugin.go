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

package numainterpodaffinity

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"k8s.io/kubernetes/pkg/scheduler/framework"
	"k8s.io/kubernetes/pkg/scheduler/framework/parallelize"
)

const (
	NameNUMAInterPodAffinity = "NUMAInterPodAffinity"

	ErrNUMAInterPodAffinityNotMatch = "node(s) didn't satisfy numa level inter pod affinity rules"
	ErrGetPreFilterState            = "getPreFilterState failed"
)

type NUMAInterPodAffinity struct {
	parallelizer parallelize.Parallelizer
	sharedLister framework.SharedLister
}

var _ framework.PreFilterPlugin = &NUMAInterPodAffinity{}
var _ framework.FilterPlugin = &NUMAInterPodAffinity{}
var _ framework.ReservePlugin = &NUMAInterPodAffinity{}

// Name returns name of the plugin. It is used in logs, etc.
func (pl *NUMAInterPodAffinity) Name() string {
	return NameNUMAInterPodAffinity
}

func New(_ runtime.Object, handle framework.Handle) (framework.Plugin, error) {
	klog.Infof("create annotationmatch plugin")
	return &NUMAInterPodAffinity{
		parallelizer: handle.Parallelizer(),
		sharedLister: handle.SnapshotSharedLister(),
	}, nil
}
