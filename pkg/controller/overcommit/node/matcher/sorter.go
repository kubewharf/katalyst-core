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

package matcher

import (
	"sort"

	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/apis/overcommit/v1alpha1"
)

type NocList []*v1alpha1.NodeOvercommitConfig

func (nl NocList) Len() int {
	return len(nl)
}

func (nl NocList) Swap(i, j int) {
	nl[i], nl[j] = nl[j], nl[i]
}

func (nl NocList) Less(i, j int) bool {
	return nl[j].CreationTimestamp.Before(&nl[i].CreationTimestamp)
}

func GetValidNodeOvercommitConfig(nocIndexer cache.Indexer, key string) (*v1alpha1.NodeOvercommitConfig, error) {
	objs, err := nocIndexer.ByIndex(LabelSelectorValIndex, key)
	if err != nil {
		if errors.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}

	nocs := make([]*v1alpha1.NodeOvercommitConfig, 0)
	for _, obj := range objs {
		noc, ok := obj.(*v1alpha1.NodeOvercommitConfig)
		if !ok {
			klog.Warningf("unknown obj from nocIndexer: %v", obj)
			continue
		}
		nocs = append(nocs, noc)
	}
	if len(nocs) == 0 {
		return nil, nil
	}
	if len(nocs) != 1 {
		sort.Sort(NocList(nocs))
	}
	return nocs[0], nil
}
