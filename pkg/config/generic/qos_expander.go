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

package generic

import (
	"sync"

	v1 "k8s.io/api/core/v1"
)

// QoSLevelExpander provides a mechanism for user-specified qos judgement
// since we may need to set qos-level for some customized cases
type QoSLevelExpander interface {
	Override(qosLevel string, pod *v1.Pod, expandedAnnotations map[string]string) (string, bool)
}

type dummyQoSLevelExpander struct{}

func (d dummyQoSLevelExpander) Override(qos string, _ *v1.Pod, _ map[string]string) (string, bool) {
	return qos, false
}

var (
	qosLevelExpander     QoSLevelExpander = dummyQoSLevelExpander{}
	qosLevelExpanderLock sync.RWMutex
)

func SetQoSLevelExpander(e QoSLevelExpander) {
	qosLevelExpanderLock.Lock()
	qosLevelExpander = e
	qosLevelExpanderLock.Unlock()
}

func getQoSLevelExpander() QoSLevelExpander {
	qosLevelExpanderLock.RLock()
	defer qosLevelExpanderLock.RUnlock()
	return qosLevelExpander
}

// QoSEnhancementExpander provides a mechanism for user-specified qos-enhancement judgement
// since we may need to set qos-enhancement for some customized cases
type QoSEnhancementExpander interface {
	Override(flattenedEnhancements map[string]string, pod *v1.Pod, expandedAnnotations map[string]string) (map[string]string, bool)
}

type dummyQoSEnhancementExpander struct{}

func (d dummyQoSEnhancementExpander) Override(flattenedEnhancements map[string]string, _ *v1.Pod, _ map[string]string) (map[string]string, bool) {
	return flattenedEnhancements, false
}

var (
	qosEnhancementExpander     QoSEnhancementExpander = dummyQoSEnhancementExpander{}
	qosEnhancementExpanderLock sync.RWMutex
)

func SetQoSEnhancementExpander(e QoSEnhancementExpander) {
	qosEnhancementExpanderLock.Lock()
	qosEnhancementExpander = e
	qosEnhancementExpanderLock.Unlock()
}

func getQoSEnhancementExpander() QoSEnhancementExpander {
	qosEnhancementExpanderLock.RLock()
	defer qosEnhancementExpanderLock.RUnlock()
	return qosEnhancementExpander
}
