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

package loadaware

import (
	"context"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

func (p *Plugin) Reserve(ctx context.Context, _ *framework.CycleState, pod *v1.Pod, nodeName string) *framework.Status {
	p.cache.addPod(nodeName, pod, time.Now())
	return nil
}

func (p *Plugin) Unreserve(ctx context.Context, _ *framework.CycleState, pod *v1.Pod, nodeName string) {
	p.cache.removePod(nodeName, pod)
}
