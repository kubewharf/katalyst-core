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

package helper

import (
	"sync"
)

// QoSLevelUpdateFunc provides a mechanism for user-specified qos judgement
// since we may need to set qos-level for some customized cases
type QoSLevelUpdateFunc func(qosLevel string, podAnnotations map[string]string) string

var qosLevelUpdater QoSLevelUpdateFunc
var qosLevelUpdaterLock sync.RWMutex

func SetQoSLevelUpdateFunc(f QoSLevelUpdateFunc) {
	qosLevelUpdaterLock.Lock()
	qosLevelUpdater = f
	qosLevelUpdaterLock.Unlock()
}

func GetQoSLevelUpdateFunc() QoSLevelUpdateFunc {
	qosLevelUpdaterLock.RLock()
	defer qosLevelUpdaterLock.RUnlock()
	return qosLevelUpdater
}
