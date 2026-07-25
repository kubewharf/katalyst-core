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

package topology

import "github.com/kubewharf/katalyst-core/pkg/util/machine"

type cpusetDomain string

const (
	cpusetDomainUnknown cpusetDomain = ""
	cpusetDomainPrimary cpusetDomain = "primary"
	cpusetDomainReclaim cpusetDomain = "reclaim"
)

func domainOf(role TopoNodeRole) cpusetDomain {
	switch role {
	case TopoNodeRolePrimary:
		return cpusetDomainPrimary
	case TopoNodeRoleReclaim, TopoNodeRoleReclaimNUMABucket, TopoNodeRoleReclaimSibling:
		return cpusetDomainReclaim
	default:
		return cpusetDomainUnknown
	}
}

func isPrimaryRole(role TopoNodeRole) bool {
	return domainOf(role) == cpusetDomainPrimary
}

func isReclaimRole(role TopoNodeRole) bool {
	return domainOf(role) == cpusetDomainReclaim
}

func domainNodes(dag *TopoDAG, domain cpusetDomain) []*TopoNode {
	if dag == nil || domain == cpusetDomainUnknown {
		return nil
	}
	nodes := make([]*TopoNode, 0)
	for _, node := range dag.Nodes() {
		if domainOf(node.Role) == domain {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

func domainTargetUnion(nodes []*TopoNode, targets map[string]machine.CPUSet) machine.CPUSet {
	out := machine.NewCPUSet()
	for _, node := range nodes {
		if node == nil {
			continue
		}
		out = out.Union(targets[node.Rel])
	}
	return out
}
