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

package staticpolicy

import (
	"fmt"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/staticpolicy/cni"
)

// WithCNIPeriodicNodeUpdater injects the optional updater before policy startup.
func WithCNIPeriodicNodeUpdater(plugin skeleton.QRMPlugin, updater *cni.CNIPeriodicNodeUpdater) error {
	p, ok := plugin.(*StaticPolicy)
	if !ok || p == nil {
		return fmt.Errorf("CNI Node updater requires a nonnil network StaticPolicy, got %T", plugin)
	}
	p.Lock()
	defer p.Unlock()
	if p.started {
		return fmt.Errorf("cannot inject CNI Node updater into started policy %q", p.name)
	}
	p.cniNodeUpdater = updater
	return nil
}
