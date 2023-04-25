// Copyright 2022 The Katalyst Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package qos

import (
	"strconv"

	v1 "k8s.io/api/core/v1"
)

// GetPodNetClassID parses net class id for the given pod.
// if the given pod doesn't specify a class id, the first value returned will be false
func GetPodNetClassID(pod *v1.Pod, podLevelNetClassAnnoKey string) (bool, uint32, error) {
	classIDStr, ok := pod.GetAnnotations()[podLevelNetClassAnnoKey]

	if !ok {
		return false, 0, nil
	}

	classID, err := strconv.ParseUint(classIDStr, 10, 64)
	if err != nil {
		return true, 0, err
	}
	return true, uint32(classID), nil
}
