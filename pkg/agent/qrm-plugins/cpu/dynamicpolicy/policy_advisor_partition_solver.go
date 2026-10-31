/*
Copyright 2026 The Katalyst Authors.

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

package dynamicpolicy

import (
	"container/heap"
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type partitionDemand struct {
	key             string
	requestGroupKey string
	quantity        int
	requestQuantity float64
	eligible        machine.CPUSet
	preferred       machine.CPUSet
	class           advisorBlockClass

	// targetDriven marks a target-driven reclaim dedicated source. targetDriven
	// sources may shrink to their frozen sourceTarget (instead of preserving
	// before==after ownership), but may never go below it. Legacy sources keep
	// the old invariant.
	targetDriven bool
	sourceTarget int
	reclaimQuota int // frozen reclaim source quota for targetDriven donors
	numaID       int // NUMA id for domain-aware selection
}

type partitionFlowEdge struct {
	to      int
	reverse int
	cap     int
	cost    int64
}

type partitionAssignmentEdge struct {
	demand int
	edge   int
}

const (
	// The edge budget admits a fully connected 1024 CPU x 1024 demand graph,
	// while bounding memory before an unexpectedly large eligibility matrix is built.
	partitionAssignmentEdgeBudget = 1024 * 1024
	// Every residual-edge examination in shortest-path and admissible-flow scans
	// consumes this budget, bounding pathological successive-shortest-path inputs.
	partitionFlowOperationBudget = 100_000_000
)

var (
	errPartitionAssignmentEdgeBudget = errors.New("partition graph edge budget exceeded")
	errPartitionFlowOperationBudget  = errors.New("partition flow operation budget exceeded")
	errPartitionNoFeasibleAssignment = errors.New("partition demands have no feasible assignment")
)

type partitionGraphBudget struct {
	maxAssignmentEdges int
	assignmentEdges    int
	// uniformShortcutBypassed disables the strict uniform fast-path for this one
	// solve call only. It defaults to false (fast-path enabled) so production
	// callers are unchanged; tests set it to diff the fast-path against the
	// general min-cost-flow solver without mutating a shared package global,
	// which would race against concurrently running parallel solver tests.
	uniformShortcutBypassed bool
}

type partitionSearchBudget struct {
	maxFlowOperations int
	flowOperations    int
}

func defaultPartitionGraphBudget() partitionGraphBudget {
	return partitionGraphBudget{maxAssignmentEdges: partitionAssignmentEdgeBudget}
}

func defaultPartitionSearchBudget() partitionSearchBudget {
	return partitionSearchBudget{maxFlowOperations: partitionFlowOperationBudget}
}

type partitionDistanceItem struct {
	node     int
	distance int64
}

type partitionDistanceQueue []partitionDistanceItem

func (q partitionDistanceQueue) Len() int { return len(q) }

func (q partitionDistanceQueue) Less(i, j int) bool {
	if q[i].distance != q[j].distance {
		return q[i].distance < q[j].distance
	}
	return q[i].node < q[j].node
}

func (q partitionDistanceQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }

func (q *partitionDistanceQueue) Push(value interface{}) {
	*q = append(*q, value.(partitionDistanceItem))
}

func (q *partitionDistanceQueue) Pop() interface{} {
	old := *q
	last := len(old) - 1
	value := old[last]
	*q = old[:last]
	return value
}

func solveDisjointPartitions(
	demands []partitionDemand,
	topology *machine.CPUTopology,
) (map[string]machine.CPUSet, error) {
	graphBudget := defaultPartitionGraphBudget()
	searchBudget := defaultPartitionSearchBudget()
	return solveDisjointPartitionsWithBudgets(
		demands, topology, &graphBudget, &searchBudget)
}

func solveDisjointPartitionsWithBudgets(
	demands []partitionDemand,
	topology *machine.CPUTopology,
	graphBudget *partitionGraphBudget,
	searchBudget *partitionSearchBudget,
) (map[string]machine.CPUSet, error) {
	if graphBudget == nil || graphBudget.maxAssignmentEdges <= 0 {
		return nil, errPartitionAssignmentEdgeBudget
	}
	if searchBudget == nil || searchBudget.maxFlowOperations <= 0 {
		return nil, errPartitionFlowOperationBudget
	}
	sortedDemands, cpus, total, err := validatePartitionDemands(demands, topology)
	if err != nil {
		return nil, err
	}
	return solveValidatedDisjointPartitions(
		sortedDemands, cpus, total, topology, graphBudget, searchBudget)
}

func solveValidatedDisjointPartitions(
	sortedDemands []partitionDemand,
	cpus []int,
	total int,
	topology *machine.CPUTopology,
	graphBudget *partitionGraphBudget,
	searchBudget *partitionSearchBudget,
) (map[string]machine.CPUSet, error) {
	result := make(map[string]machine.CPUSet, len(sortedDemands))
	for _, demand := range sortedDemands {
		result[demand.key] = machine.NewCPUSet()
	}
	if total == 0 {
		return result, nil
	}

	// Deterministic O(demands) fast-path for the strict uniform-eligibility /
	// uniform-cost complete-match case. Every eligible edge carries the same
	// tie-free cost, so any perfect matching is equally optimal; pairing the
	// i-th sorted demand with the i-th sorted CPU yields a deterministic optimum
	// without building the O(cpus*demands) residual graph or running the flow
	// search. The assignment-edge budget is still enforced up front.
	if !graphBudget.uniformShortcutBypassed && solveUniformShortcut(result, sortedDemands, cpus, total, graphBudget) {
		return result, nil
	}

	source := 0
	cpuBase := 1
	demandBase := cpuBase + len(cpus)
	sink := demandBase + len(sortedDemands)
	// Pre-size every adjacency slice to its upper bound so the dense bipartite
	// graph build does not repeatedly grow and re-copy slices (which dominates
	// allocation and is amplified under the race detector).
	graph := make([][]partitionFlowEdge, sink+1)
	graph[source] = make([]partitionFlowEdge, 0, len(cpus))
	for i := range cpus {
		graph[cpuBase+i] = make([]partitionFlowEdge, 0, len(sortedDemands)+1)
	}
	for i := range sortedDemands {
		graph[demandBase+i] = make([]partitionFlowEdge, 0, len(cpus)+1)
	}
	graph[sink] = make([]partitionFlowEdge, 0, len(sortedDemands))
	assignmentEdges := make([][]partitionAssignmentEdge, len(cpus))
	for i := range cpus {
		assignmentEdges[i] = make([]partitionAssignmentEdge, 0, len(sortedDemands))
	}

	// dedicatedEligible only influences the cost charged to mandatory-reclaim
	// demands. When no reclaim demand exists the union is never consulted, so
	// skip the O(cpus*demands) merge and pass an empty set instead.
	dedicatedEligible := machine.NewCPUSet()
	hasReclaim := false
	for i := range sortedDemands {
		if sortedDemands[i].class == advisorBlockClassMandatoryReclaim {
			hasReclaim = true
			break
		}
	}
	if hasReclaim {
		for _, demand := range sortedDemands {
			if demand.class == advisorBlockClassDedicated {
				dedicatedEligible.AddSet(demand.eligible)
			}
		}
	}
	oldWeight, reclaimWeight, topologyWeight, err := partitionCostWeights(total, len(cpus), len(sortedDemands))
	if err != nil {
		return nil, err
	}

	for cpuIndex, cpu := range cpus {
		cpuNode := cpuBase + cpuIndex
		addPartitionFlowEdge(graph, source, cpuNode, 1, 0)
		for demandIndex, demand := range sortedDemands {
			if !demand.eligible.Contains(cpu) {
				continue
			}
			graphBudget.assignmentEdges++
			if graphBudget.assignmentEdges > graphBudget.maxAssignmentEdges {
				return nil, errPartitionAssignmentEdgeBudget
			}
			cost, costErr := partitionEdgeCost(
				cpu, cpuIndex, demandIndex, demand, dedicatedEligible, topology,
				oldWeight, reclaimWeight, topologyWeight, len(sortedDemands), total < len(cpus),
			)
			if costErr != nil {
				return nil, costErr
			}
			edgeIndex := len(graph[cpuNode])
			addPartitionFlowEdge(graph, cpuNode, demandBase+demandIndex, 1, cost)
			assignmentEdges[cpuIndex] = append(assignmentEdges[cpuIndex], partitionAssignmentEdge{
				demand: demandIndex,
				edge:   edgeIndex,
			})
		}
	}
	for demandIndex, demand := range sortedDemands {
		addPartitionFlowEdge(graph, demandBase+demandIndex, sink, demand.quantity, 0)
	}

	flow, err := partitionMinCostFlowWithUsage(
		graph,
		source,
		sink,
		total,
		&searchBudget.flowOperations,
		searchBudget.maxFlowOperations,
	)
	if err != nil {
		return nil, err
	}
	if flow != total {
		return nil, errPartitionNoFeasibleAssignment
	}
	for cpuIndex, edges := range assignmentEdges {
		cpuNode := cpuBase + cpuIndex
		for _, assignment := range edges {
			if graph[cpuNode][assignment.edge].cap == 0 {
				key := sortedDemands[assignment.demand].key
				assigned := result[key]
				assigned.Add(cpus[cpuIndex])
				result[key] = assigned
				break
			}
		}
	}
	return result, nil
}

// solveUniformShortcut applies the strict deterministic fast-path and reports
// whether it took. The condition is deliberately narrow:
//   - every demand has quantity 1, class dedicated, and empty preferred;
//   - every demand's eligible set is identical to the first demand's;
//   - len(cpus) == len(sortedDemands) == total.
//
// Under these conditions every eligible edge has the same tie-free cost (the
// complete-assignment case suppresses the cpu/demand-rank tie-break), so any
// perfect matching is cost-optimal. Pairing sorted demands with sorted CPUs is
// deterministic and equals the general solver's greedy blocking-flow result.
func solveUniformShortcut(
	result map[string]machine.CPUSet,
	sortedDemands []partitionDemand,
	cpus []int,
	total int,
	graphBudget *partitionGraphBudget,
) bool {
	if len(sortedDemands) == 0 || total != len(cpus) || len(cpus) != len(sortedDemands) {
		return false
	}
	ref := sortedDemands[0]
	for i := range sortedDemands {
		d := sortedDemands[i]
		if d.quantity != 1 || d.class != advisorBlockClassDedicated ||
			!d.preferred.IsEmpty() || !d.eligible.Equals(ref.eligible) {
			return false
		}
	}
	edgeCount := len(cpus) * len(sortedDemands)
	graphBudget.assignmentEdges += edgeCount
	if graphBudget.assignmentEdges > graphBudget.maxAssignmentEdges {
		return false
	}
	for i := range sortedDemands {
		result[sortedDemands[i].key].Add(cpus[i])
	}
	return true
}

func validatePartitionDemands(
	demands []partitionDemand,
	topology *machine.CPUTopology,
) ([]partitionDemand, []int, int, error) {
	if topology == nil {
		return nil, nil, 0, fmt.Errorf("partition topology is nil")
	}

	sortedDemands := sortedByKey(demands, func(d partitionDemand) string { return d.key })

	// Accumulate the union of all eligible CPUs with a CPU-id presence array
	// instead of growing a CPUSet map: the dense probe fans out over
	// O(cpus*demands) elements, and map rehashing/assignment is the dominant
	// cost (amplified under the race detector). The presence array keeps the
	// union build to plain indexed writes, grown lazily as cpu ids are seen.
	seen := make([]bool, topology.NumCPUs)
	eligibleUnion := make([]int, 0, topology.NumCPUs)

	total := 0
	for i, demand := range sortedDemands {
		if demand.key == "" {
			return nil, nil, 0, fmt.Errorf("partition demand has empty key")
		}
		if i > 0 && sortedDemands[i-1].key == demand.key {
			return nil, nil, 0, fmt.Errorf("partition demand has duplicate key %q", demand.key)
		}
		if demand.quantity < 0 {
			return nil, nil, 0, fmt.Errorf("partition demand %q has negative quantity", demand.key)
		}
		if demand.class != advisorBlockClassDedicated &&
			demand.class != advisorBlockClassMandatoryReclaim &&
			demand.class != advisorBlockClassShared {
			return nil, nil, 0, fmt.Errorf("partition demand %q has unsupported class %q", demand.key, demand.class)
		}
		if demand.eligible.Size() < demand.quantity {
			return nil, nil, 0, errPartitionNoFeasibleAssignment
		}
		missingCPU := -1
		demand.eligible.Each(func(cpu int) {
			if cpu >= len(seen) {
				grown := make([]bool, cpu+1)
				copy(grown, seen)
				seen = grown
			}
			// Only validate the topology membership once per distinct CPU;
			// repeated demands share eligible sets and re-checking the same
			// CPU across demands is pure overhead under the race detector.
			if seen[cpu] {
				return
			}
			if _, ok := topology.CPUDetails[cpu]; !ok && missingCPU < 0 {
				missingCPU = cpu
			}
			seen[cpu] = true
			eligibleUnion = append(eligibleUnion, cpu)
		})
		if missingCPU >= 0 {
			return nil, nil, 0, fmt.Errorf("partition demand %q references CPU %d missing from topology", demand.key, missingCPU)
		}
		total += demand.quantity
		if total < 0 {
			return nil, nil, 0, fmt.Errorf("partition demand quantity overflow")
		}
	}
	sort.Ints(eligibleUnion)
	if len(eligibleUnion) < total {
		return nil, nil, 0, errPartitionNoFeasibleAssignment
	}
	return sortedDemands, eligibleUnion, total, nil
}

func partitionCostWeights(total, cpuCount, demandCount int) (int64, int64, int64, error) {
	demandBase, err := checkedPartitionCostAdd(int64(demandCount), 1)
	if err != nil {
		return 0, 0, 0, err
	}
	maxTiePerEdge, err := checkedPartitionCostMul(int64(cpuCount), demandBase)
	if err != nil {
		return 0, 0, 0, err
	}
	maxTiePerEdge, err = checkedPartitionCostAdd(maxTiePerEdge, demandBase)
	if err != nil {
		return 0, 0, 0, err
	}
	maxTieTotal, err := checkedPartitionCostMul(int64(total), maxTiePerEdge)
	if err != nil {
		return 0, 0, 0, err
	}
	topologyWeight, err := checkedPartitionCostAdd(maxTieTotal, 1)
	if err != nil {
		return 0, 0, 0, err
	}
	maxTopologyTotal, err := checkedPartitionCostMul(int64(total), 3)
	if err != nil {
		return 0, 0, 0, err
	}
	maxTopologyTotal, err = checkedPartitionCostMul(maxTopologyTotal, topologyWeight)
	if err != nil {
		return 0, 0, 0, err
	}
	reclaimWeight, err := checkedPartitionCostAdd(maxTopologyTotal, maxTieTotal)
	if err != nil {
		return 0, 0, 0, err
	}
	reclaimWeight, err = checkedPartitionCostAdd(reclaimWeight, 1)
	if err != nil {
		return 0, 0, 0, err
	}
	maxReclaimTotal, err := checkedPartitionCostMul(int64(total), reclaimWeight)
	if err != nil {
		return 0, 0, 0, err
	}
	oldWeight, err := checkedPartitionCostAdd(maxReclaimTotal, maxTopologyTotal)
	if err != nil {
		return 0, 0, 0, err
	}
	oldWeight, err = checkedPartitionCostAdd(oldWeight, maxTieTotal)
	if err != nil {
		return 0, 0, 0, err
	}
	oldWeight, err = checkedPartitionCostAdd(oldWeight, 1)
	if err != nil {
		return 0, 0, 0, err
	}
	return oldWeight, reclaimWeight, topologyWeight, nil
}

func partitionEdgeCost(
	cpu, cpuRank, demandRank int,
	demand partitionDemand,
	dedicatedEligible machine.CPUSet,
	topology *machine.CPUTopology,
	oldWeight, reclaimWeight, topologyWeight int64,
	demandCount int,
	includeTie bool,
) (int64, error) {
	var cost int64
	var err error
	if !demand.preferred.Contains(cpu) {
		cost, err = checkedPartitionCostAdd(cost, oldWeight)
		if err != nil {
			return 0, err
		}
	}
	if demand.class == advisorBlockClassMandatoryReclaim && dedicatedEligible.Contains(cpu) {
		cost, err = checkedPartitionCostAdd(cost, reclaimWeight)
		if err != nil {
			return 0, err
		}
	}
	topologyCost, err := checkedPartitionCostMul(
		int64(partitionTopologyDistance(cpu, demand.preferred, topology)),
		topologyWeight,
	)
	if err != nil {
		return 0, err
	}
	cost, err = checkedPartitionCostAdd(cost, topologyCost)
	if err != nil {
		return 0, err
	}
	// When every eligible CPU must be assigned, the sum of CPU and demand ranks
	// is constant across feasible solutions. Omitting that constant exposes all
	// equal-cost paths to the blocking-flow phase without changing the optimum.
	if !includeTie {
		return cost, nil
	}
	demandBase, err := checkedPartitionCostAdd(int64(demandCount), 1)
	if err != nil {
		return 0, err
	}
	tieCost, err := checkedPartitionCostMul(int64(cpuRank), demandBase)
	if err != nil {
		return 0, err
	}
	demandTie, err := checkedPartitionCostAdd(int64(demandRank), 1)
	if err != nil {
		return 0, err
	}
	tieCost, err = checkedPartitionCostAdd(tieCost, demandTie)
	if err != nil {
		return 0, err
	}
	return checkedPartitionCostAdd(cost, tieCost)
}

func checkedPartitionCostAdd(left, right int64) (int64, error) {
	if (right > 0 && left > math.MaxInt64-right) || (right < 0 && left < math.MinInt64-right) {
		return 0, fmt.Errorf("partition cost overflow")
	}
	return left + right, nil
}

func checkedPartitionCostMul(left, right int64) (int64, error) {
	if left == 0 || right == 0 {
		return 0, nil
	}
	if (left == math.MinInt64 && right == -1) || (right == math.MinInt64 && left == -1) {
		return 0, fmt.Errorf("partition cost overflow")
	}
	product := left * right
	if product/right != left {
		return 0, fmt.Errorf("partition cost overflow")
	}
	return product, nil
}

func partitionTopologyDistance(cpu int, preferred machine.CPUSet, topology *machine.CPUTopology) int {
	candidate := topology.CPUDetails[cpu]
	distance := 3
	if preferred.IsEmpty() {
		return distance
	}
	for _, oldCPU := range preferred.ToSliceInt() {
		old, ok := topology.CPUDetails[oldCPU]
		if !ok {
			continue
		}
		switch {
		case candidate.SocketID == old.SocketID && candidate.CoreID == old.CoreID:
			return 0
		case candidate.NUMANodeID == old.NUMANodeID:
			if distance > 1 {
				distance = 1
			}
		case candidate.SocketID == old.SocketID:
			if distance > 2 {
				distance = 2
			}
		}
	}
	return distance
}

func addPartitionFlowEdge(graph [][]partitionFlowEdge, from, to, cap int, cost int64) {
	forward := partitionFlowEdge{to: to, reverse: len(graph[to]), cap: cap, cost: cost}
	reverse := partitionFlowEdge{to: from, reverse: len(graph[from]), cap: 0, cost: -cost}
	graph[from] = append(graph[from], forward)
	graph[to] = append(graph[to], reverse)
}

func partitionMinCostFlowWithUsage(
	graph [][]partitionFlowEdge,
	source, sink, wanted int,
	operations *int,
	maxOperations int,
) (int, error) {
	flow := 0
	potential := make([]int64, len(graph))
	for flow < wanted {
		distance := make([]int64, len(graph))
		for i := range distance {
			distance[i] = math.MaxInt64
		}
		distance[source] = 0
		queue := partitionDistanceQueue{{node: source, distance: 0}}
		heap.Init(&queue)

		for queue.Len() > 0 {
			item := heap.Pop(&queue).(partitionDistanceItem)
			if item.distance != distance[item.node] {
				continue
			}
			for _, edge := range graph[item.node] {
				*operations++
				if *operations > maxOperations {
					return 0, errPartitionFlowOperationBudget
				}
				if edge.cap == 0 {
					continue
				}
				reducedCost, err := partitionReducedCost(item.node, edge, potential)
				if err != nil {
					return 0, err
				}
				if reducedCost < 0 {
					return 0, fmt.Errorf("partition reduced cost became negative")
				}
				nextDistance, err := checkedPartitionCostAdd(item.distance, reducedCost)
				if err != nil {
					return 0, err
				}
				if nextDistance < distance[edge.to] {
					distance[edge.to] = nextDistance
					heap.Push(&queue, partitionDistanceItem{node: edge.to, distance: nextDistance})
				}
			}
		}
		if distance[sink] == math.MaxInt64 {
			break
		}

		for node := range potential {
			if distance[node] == math.MaxInt64 {
				continue
			}
			nextPotential, err := checkedPartitionCostAdd(potential[node], distance[node])
			if err != nil {
				return 0, err
			}
			potential[node] = nextPotential
		}

		pushed, err := partitionPushAdmissibleFlow(
			graph, source, sink, wanted-flow, potential, operations, maxOperations,
		)
		if err != nil {
			return 0, err
		}
		if pushed == 0 {
			return 0, fmt.Errorf("partition shortest path produced no admissible flow")
		}
		flow += pushed
	}
	return flow, nil
}

func partitionPushAdmissibleFlow(
	graph [][]partitionFlowEdge,
	source, sink, wanted int,
	potential []int64,
	operations *int,
	maxOperations int,
) (int, error) {
	total := 0
	for total < wanted {
		level := make([]int, len(graph))
		for node := range level {
			level[node] = -1
		}
		level[source] = 0
		queue := make([]int, 1, len(graph))
		queue[0] = source
		for head := 0; head < len(queue); head++ {
			node := queue[head]
			for _, edge := range graph[node] {
				*operations++
				if *operations > maxOperations {
					return 0, errPartitionFlowOperationBudget
				}
				if edge.cap == 0 || level[edge.to] != -1 {
					continue
				}
				reducedCost, err := partitionReducedCost(node, edge, potential)
				if err != nil {
					return 0, err
				}
				if reducedCost != 0 {
					continue
				}
				level[edge.to] = level[node] + 1
				queue = append(queue, edge.to)
			}
		}
		if level[sink] == -1 {
			break
		}

		nextEdge := make([]int, len(graph))
		for total < wanted {
			pushed, err := partitionPushAdmissiblePath(
				graph, source, sink, wanted-total, potential, level, nextEdge, operations, maxOperations,
			)
			if err != nil {
				return 0, err
			}
			if pushed == 0 {
				break
			}
			total += pushed
		}
	}
	return total, nil
}

func partitionPushAdmissiblePath(
	graph [][]partitionFlowEdge,
	node, sink, available int,
	potential []int64,
	level, nextEdge []int,
	operations *int,
	maxOperations int,
) (int, error) {
	if node == sink {
		return available, nil
	}
	for nextEdge[node] < len(graph[node]) {
		edgeIndex := nextEdge[node]
		edge := graph[node][edgeIndex]
		*operations++
		if *operations > maxOperations {
			return 0, errPartitionFlowOperationBudget
		}
		if edge.cap > 0 && level[edge.to] == level[node]+1 {
			reducedCost, err := partitionReducedCost(node, edge, potential)
			if err != nil {
				return 0, err
			}
			if reducedCost == 0 {
				pathAvailable := available
				if edge.cap < pathAvailable {
					pathAvailable = edge.cap
				}
				pushed, err := partitionPushAdmissiblePath(
					graph, edge.to, sink, pathAvailable,
					potential, level, nextEdge, operations, maxOperations,
				)
				if err != nil {
					return 0, err
				}
				if pushed > 0 {
					reverse := edge.reverse
					graph[node][edgeIndex].cap -= pushed
					graph[edge.to][reverse].cap += pushed
					return pushed, nil
				}
			}
		}
		nextEdge[node]++
	}
	return 0, nil
}

func partitionReducedCost(from int, edge partitionFlowEdge, potential []int64) (int64, error) {
	reducedCost, err := checkedPartitionCostAdd(edge.cost, potential[from])
	if err != nil {
		return 0, err
	}
	return checkedPartitionCostSub(reducedCost, potential[edge.to])
}

func checkedPartitionCostSub(left, right int64) (int64, error) {
	if (right > 0 && left < math.MinInt64+right) || (right < 0 && left > math.MaxInt64+right) {
		return 0, fmt.Errorf("partition cost overflow")
	}
	return left - right, nil
}
