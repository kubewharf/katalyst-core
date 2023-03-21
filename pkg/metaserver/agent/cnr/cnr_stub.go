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

package cnr

import (
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

// CNRNotifierStub is a stub implementation of CNRNotifier.
type CNRNotifierStub struct{}

// OnCNRUpdate is called when CNR is updated.
func (C CNRNotifierStub) OnCNRUpdate(cnr *v1alpha1.CustomNodeResource) {
	klog.Infof("CNRNotifierStub.OnCNRUpdate: %#v", cnr)
}

// OnCNRStatusUpdate is called when CNR status is updated.
func (C CNRNotifierStub) OnCNRStatusUpdate(cnr *v1alpha1.CustomNodeResource) {
	klog.Infof("CNRNotifierStub.OnCNRUpdate: %#v", cnr)
}
