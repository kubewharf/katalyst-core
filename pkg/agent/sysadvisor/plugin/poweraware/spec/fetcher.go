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

package spec

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
)

type SpecFetcher interface {
	GetPowerSpec(ctx context.Context) (*PowerSpec, error)
}

type specFetcherByNodeAnnotation struct {
	nodeFetcher node.NodeFetcher
}

func (s specFetcherByNodeAnnotation) GetPowerSpec(ctx context.Context) (*PowerSpec, error) {
	nodeObj, err := s.nodeFetcher.GetNode(ctx)
	if err != nil {
		return nil, err
	}

	return GetPowerSpec(nodeObj)
}

func NewFetcher(nodeFetcher node.NodeFetcher) SpecFetcher {
	return specFetcherByNodeAnnotation{nodeFetcher: nodeFetcher}
}
