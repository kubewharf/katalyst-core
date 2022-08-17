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

package algorithm

import (
	corev1 "k8s.io/api/core/v1"
	"sync"

	apis "github.com/kubewharf/katalyst-api/pkg/apis/autoscaling/v1alpha1"
	workload "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
)

// ResourceRecommender is used as a common interface for in-tree VPA algorithms;
// all in-tree implementations should implement those functions defined here.
type ResourceRecommender interface {
	Name() string

	// GetRecommendedPodResources calculate the recommended resources for given SPD
	GetRecommendedPodResources(spd *workload.ServiceProfileDescriptor, pods []*corev1.Pod) (
		[]apis.RecommendedPodResources, []apis.RecommendedContainerResources, error)
}

var recommenderMap sync.Map

// RegisterRecommender indicates that all in-tree algorithm implementations should be registered here,
// so that the main VPA process can walk through those algorithms to produce th final resources.
func RegisterRecommender(p ResourceRecommender) {
	recommenderMap.Store(p.Name(), p)
}

// GetRecommender returns those recommenders that have been registered
func GetRecommender() map[string]ResourceRecommender {
	result := make(map[string]ResourceRecommender)
	recommenderMap.Range(func(key, value interface{}) bool {
		result[key.(string)] = value.(ResourceRecommender)
		return true
	})
	return result
}
