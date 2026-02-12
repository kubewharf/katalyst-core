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

package resource

import (
	"context"
	"fmt"
	"sync"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

type ResourceAdvisorStub struct {
	sync.Mutex
	resources  map[v1.ResourceName]resource.Quantity
	subAdvisor map[types.QoSResourceName]*SubResourceAdvisorStub
}

var _ ResourceAdvisor = NewResourceAdvisorStub()

func NewResourceAdvisorStub() *ResourceAdvisorStub {
	sub := make(map[types.QoSResourceName]*SubResourceAdvisorStub)
	sub[types.QoSResourceCPU] = NewSubResourceAdvisorStub()
	sub[types.QoSResourceMemory] = NewSubResourceAdvisorStub()
	return &ResourceAdvisorStub{
		resources:  make(map[v1.ResourceName]resource.Quantity),
		subAdvisor: sub,
	}
}

func (r *ResourceAdvisorStub) Run(ctx context.Context) {
}

func (r *ResourceAdvisorStub) GetSubAdvisor(resourceName types.QoSResourceName) (SubResourceAdvisor, error) {
	return r.subAdvisor[resourceName], nil
}

func (r *ResourceAdvisorStub) GetHeadroom(resourceName v1.ResourceName) (resource.Quantity, map[int]resource.Quantity, error) {
	r.Lock()
	defer r.Unlock()

	if quantity, ok := r.resources[resourceName]; ok {
		return quantity, nil, nil
	}
	return resource.Quantity{}, nil, fmt.Errorf("not exist")
}

func (r *ResourceAdvisorStub) SetHeadroom(resourceName v1.ResourceName, quantity resource.Quantity) {
	r.Lock()
	defer r.Unlock()

	if sub, ok := r.subAdvisor[types.QoSResourceName(resourceName)]; ok {
		sub.SetHeadroom(quantity)
	}
}

type SubResourceAdvisorStub struct {
	sync.Mutex
	quantity resource.Quantity
}

var _ SubResourceAdvisor = NewSubResourceAdvisorStub()

func NewSubResourceAdvisorStub() *SubResourceAdvisorStub {
	return &SubResourceAdvisorStub{
		quantity: resource.MustParse("0"),
	}
}

func (s *SubResourceAdvisorStub) Name() string {
	return "stub-sub-resource-advisor"
}

func (s *SubResourceAdvisorStub) Run(ctx context.Context) {
}

func (s *SubResourceAdvisorStub) UpdateAndGetAdvice(_ context.Context) (interface{}, error) {
	return nil, nil
}

func (s *SubResourceAdvisorStub) GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error) {
	s.Lock()
	defer s.Unlock()

	return s.quantity, nil, nil
}

func (s *SubResourceAdvisorStub) SetHeadroom(quantity resource.Quantity) {
	s.Lock()
	defer s.Unlock()

	s.quantity = quantity
}
