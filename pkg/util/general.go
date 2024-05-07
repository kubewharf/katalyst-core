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

// util package will be arranged as follows
// - utils for extended crd will be put in top-level dir
// 	 - for each crd, put functions in individual file(s)
//   - for general utils, put functions in this file
// - utils for specific utils, put them in individual dir(s)
//   - e.g. cgroup/native-objects ...

package util

import (
	"sync"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
)

// WorkloadSPDEnabledFunc checks if the given workload is enabled with service profiling.
type WorkloadSPDEnabledFunc func(metav1.Object) bool

var (
	workloadSPDEnabledLock sync.RWMutex
	workloadSPDEnabled     WorkloadSPDEnabledFunc = func(workload metav1.Object) bool {
		return workload.GetAnnotations()[apiconsts.WorkloadAnnotationSPDEnableKey] == apiconsts.WorkloadAnnotationSPDEnabled
	}
)

// SetWorkloadEnableFunc provides a way to set the
func SetWorkloadEnableFunc(f WorkloadSPDEnabledFunc) {
	workloadSPDEnabledLock.Lock()
	defer workloadSPDEnabledLock.Unlock()
	workloadSPDEnabled = f
}

func WorkloadSPDEnabled(workload metav1.Object) bool {
	workloadSPDEnabledLock.RLock()
	defer workloadSPDEnabledLock.RUnlock()
	return workloadSPDEnabled(workload)
}
