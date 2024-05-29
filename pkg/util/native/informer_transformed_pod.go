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

package native

import (
	"fmt"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/tools/cache"
)

type PodTransformerFunc func(src, dest *corev1.Pod)

var (
	podTransformers   []PodTransformerFunc
	podTransformerMtx sync.RWMutex
)

func SetPodTransformer(f PodTransformerFunc) {
	podTransformerMtx.Lock()
	defer podTransformerMtx.Unlock()
	podTransformers = append(podTransformers, f)
}

func GetPodTransformer() (cache.TransformFunc, bool) {
	podTransformerMtx.RLock()
	defer podTransformerMtx.RUnlock()

	if len(podTransformers) == 0 {
		return nil, false
	}

	return func(obj interface{}) (interface{}, error) {
		pod, ok := obj.(*corev1.Pod)
		if !ok {
			return nil, fmt.Errorf("unexpected object type: %T", obj)
		}

		returnedPod := &corev1.Pod{
			TypeMeta:   pod.TypeMeta,
			ObjectMeta: pod.ObjectMeta,
		}
		for _, f := range podTransformers {
			f(pod, returnedPod)
		}
		return returnedPod, nil
	}, true
}
