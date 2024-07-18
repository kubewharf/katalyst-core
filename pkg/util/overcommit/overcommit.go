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

package overcommit

import (
	"fmt"
	"strconv"

	"k8s.io/klog/v2"
)

func OvercommitRatioValidate(
	nodeAnnotation map[string]string,
	setOvercommitKey, predictOvercommitKey, realtimeOvercommitKey string,
	enableDynamicOvercommit bool,
) (float64, error) {
	// overcommit is not allowed if overcommitRatio is not set by user
	setOvercommitVal, ok := nodeAnnotation[setOvercommitKey]
	if !ok {
		return 1.0, nil
	}

	overcommitRatio, err := strconv.ParseFloat(setOvercommitVal, 64)
	if err != nil {
		return 1.0, err
	}

	if !enableDynamicOvercommit {
		return overcommitRatio, nil
	}

	predictOvercommitVal, ok := nodeAnnotation[predictOvercommitKey]
	if ok {
		predictOvercommitRatio, err := strconv.ParseFloat(predictOvercommitVal, 64)
		if err != nil {
			klog.Errorf("predict overcommit %s validate fail: %v", predictOvercommitVal, err)
		}
		if predictOvercommitRatio < overcommitRatio {
			overcommitRatio = predictOvercommitRatio
		}
	}

	realtimeOvercommitVal, ok := nodeAnnotation[realtimeOvercommitKey]
	if ok {
		realtimeOvercommitRatio, err := strconv.ParseFloat(realtimeOvercommitVal, 64)
		if err != nil {
			klog.Errorf("realtime overcommit %s validate fail: %v", realtimeOvercommitVal, err)
		}
		if realtimeOvercommitRatio < overcommitRatio {
			overcommitRatio = realtimeOvercommitRatio
		}
	}

	if overcommitRatio < 1.0 {
		err = fmt.Errorf("overcommitRatio should be greater than 1")
		klog.Error(err)
		return 1.0, nil
	}

	return overcommitRatio, nil
}
