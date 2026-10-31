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
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type partitionResidualDigest [sha256.Size]byte

type partitionResidualCanonicalDemand struct {
	Key             string `json:"key"`
	RequestGroupKey string `json:"requestGroupKey"`
	Quantity        int    `json:"quantity"`
	RequestQuantity string `json:"requestQuantity"`
	Class           string `json:"class"`
}

type partitionResidualSolveResult struct {
	assignments          map[string]machine.CPUSet
	canonicalAssignments map[string]map[string]int
	infeasible           bool
}

type partitionResidualPreparedPayload struct {
	payload string
}

type partitionResidualSolveCacheEntry struct {
	prepared *partitionResidualPreparedPayload
	result   partitionResidualSolveResult
}

const (
	partitionResidualMaxPreparationGraphs = 100_000
	partitionResidualMaxCanonicalWork     = 100_000_000
	partitionResidualMaxRetainedBytes     = 64 << 20
	partitionResidualMaxEntries           = 100_000
	partitionResidualMaxResultUnits       = 1_048_576
)

var (
	errPartitionResidualPreparationWorkBudget = errors.New("partition residual cache preparation work budget exceeded")
	errPartitionResidualRetainedMemoryBudget  = errors.New("partition residual cache retained-memory budget exceeded")
)

type partitionResidualCacheLimits struct {
	maxPreparationGraphs int
	maxCanonicalWork     int
	maxRetainedBytes     int
	maxEntries           int
	maxResultUnits       int
}

type partitionResidualCacheUsage struct {
	preparationGraphs int
	canonicalWork     int
	retainedBytes     int
	entries           int
	resultUnits       int
}

type partitionResidualCacheLimitError struct {
	kind     error
	resource string
	used     int
	limit    int
}

func (e *partitionResidualCacheLimitError) Error() string {
	return fmt.Sprintf("%v (%s: used %d, limit %d)",
		e.kind, e.resource, e.used, e.limit)
}

func (e *partitionResidualCacheLimitError) Unwrap() error {
	return e.kind
}

type partitionResidualSolveCache struct {
	topology         *machine.CPUTopology
	entries          map[partitionResidualDigest][]partitionResidualSolveCacheEntry
	repeatedDigests  map[partitionResidualDigest]struct{}
	repeatedPayloads map[partitionResidualDigest]map[string]*partitionResidualPreparedPayload
	limits           partitionResidualCacheLimits
	usage            partitionResidualCacheUsage
	hits             int
	misses           int
}

func newPartitionResidualSolveCache(
	topology *machine.CPUTopology,
) (*partitionResidualSolveCache, error) {
	if topology == nil {
		return nil, fmt.Errorf("partition topology is nil")
	}
	return &partitionResidualSolveCache{
		topology:         clonePartitionResidualTopology(topology),
		entries:          make(map[partitionResidualDigest][]partitionResidualSolveCacheEntry),
		repeatedDigests:  make(map[partitionResidualDigest]struct{}),
		repeatedPayloads: make(map[partitionResidualDigest]map[string]*partitionResidualPreparedPayload),
		limits: partitionResidualCacheLimits{
			maxPreparationGraphs: partitionResidualMaxPreparationGraphs,
			maxCanonicalWork:     partitionResidualMaxCanonicalWork,
			maxRetainedBytes:     partitionResidualMaxRetainedBytes,
			maxEntries:           partitionResidualMaxEntries,
			maxResultUnits:       partitionResidualMaxResultUnits,
		},
	}, nil
}

func applyPartitionResidualCacheLimits(
	cache *partitionResidualSolveCache,
	limits partitionResidualCacheLimits,
) {
	if limits.maxPreparationGraphs > 0 {
		cache.limits.maxPreparationGraphs = limits.maxPreparationGraphs
	}
	if limits.maxCanonicalWork > 0 {
		cache.limits.maxCanonicalWork = limits.maxCanonicalWork
	}
	if limits.maxRetainedBytes > 0 {
		cache.limits.maxRetainedBytes = limits.maxRetainedBytes
	}
	if limits.maxEntries > 0 {
		cache.limits.maxEntries = limits.maxEntries
	}
	if limits.maxResultUnits > 0 {
		cache.limits.maxResultUnits = limits.maxResultUnits
	}
}

func clonePartitionResidualTopology(
	topology *machine.CPUTopology,
) *machine.CPUTopology {
	cloned := *topology
	cloned.CPUDetails = make(machine.CPUDetails, len(topology.CPUDetails))
	for cpu, info := range topology.CPUDetails {
		cloned.CPUDetails[cpu] = info
	}
	if topology.NUMANodeIDToSocketID != nil {
		cloned.NUMANodeIDToSocketID = make(map[int]int, len(topology.NUMANodeIDToSocketID))
		for numaID, socketID := range topology.NUMANodeIDToSocketID {
			cloned.NUMANodeIDToSocketID[numaID] = socketID
		}
	}
	if topology.NUMAToCPUs != nil {
		cloned.NUMAToCPUs = make(machine.NUMANodeInfo, len(topology.NUMAToCPUs))
		for numaID, cpus := range topology.NUMAToCPUs {
			cloned.NUMAToCPUs[numaID] = cpus.Clone()
		}
	}
	// CPUInfo is intentionally omitted: the partition solver consumes only
	// CPUDetails. Keeping this pointer would make the supposedly immutable
	// snapshot alias caller-owned maps and nested slices.
	cloned.CPUInfo = nil
	return &cloned
}

func clonePartitionAssignments(
	in map[string]machine.CPUSet,
) map[string]machine.CPUSet {
	if in == nil {
		return nil
	}
	out := make(map[string]machine.CPUSet, len(in))
	for key, cpus := range in {
		out[key] = cpus.Clone()
	}
	return out
}

type partitionResidualCPUClassFeature struct {
	Eligible  bool `json:"eligible"`
	Preferred bool `json:"preferred"`
	Distance  int  `json:"distance"`
}

type partitionResidualCanonicalCPU struct {
	Rank     int                                `json:"rank"`
	Features []partitionResidualCPUClassFeature `json:"features"`
}

type partitionResidualCanonical struct {
	digest     partitionResidualDigest
	payload    string
	classByCPU map[int]string
	members    map[string][]int
}

type partitionResidualBoundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *partitionResidualBoundedBuffer) Write(data []byte) (int, error) {
	next := b.Len() + len(data)
	if next > b.limit {
		return 0, partitionResidualLimitError(
			errPartitionResidualRetainedMemoryBudget,
			"canonical payload bytes", next, b.limit)
	}
	return b.Buffer.Write(data)
}

func writePartitionResidualCanonical(
	writer io.Writer,
	demands []partitionDemand,
	topology *machine.CPUTopology,
	canonical *partitionResidualCanonical,
) error {
	sortedDemands := sortedByKey(demands, func(d partitionDemand) string { return d.key })
	if _, err := io.WriteString(writer, `{"demands":[`); err != nil {
		return err
	}
	allEligible := machine.NewCPUSet()
	for i, demand := range sortedDemands {
		if i > 0 {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		encoded, err := json.Marshal(partitionResidualCanonicalDemand{
			Key:             demand.key,
			RequestGroupKey: demand.requestGroupKey,
			Quantity:        demand.quantity,
			RequestQuantity: strconv.FormatFloat(demand.requestQuantity, 'g', -1, 64),
			Class:           string(demand.class),
		})
		if err != nil {
			return err
		}
		if _, err := writer.Write(encoded); err != nil {
			return err
		}
		allEligible = allEligible.Union(demand.eligible)
	}
	if _, err := io.WriteString(writer, `],"cpus":[`); err != nil {
		return err
	}
	for cpuRank, cpu := range allEligible.ToSliceInt() {
		if _, found := topology.CPUDetails[cpu]; !found {
			return fmt.Errorf(
				"partition demand eligible CPU %d is missing from topology", cpu)
		}
		features := make([]partitionResidualCPUClassFeature, len(sortedDemands))
		for i, demand := range sortedDemands {
			features[i] = partitionResidualCPUClassFeature{
				Eligible:  demand.eligible.Contains(cpu),
				Preferred: demand.preferred.Contains(cpu),
				Distance:  partitionTopologyDistance(cpu, demand.preferred, topology),
			}
		}
		cpuRecord := partitionResidualCanonicalCPU{
			Rank:     cpuRank,
			Features: features,
		}
		encoded, err := json.Marshal(cpuRecord)
		if err != nil {
			return err
		}
		if cpuRank > 0 {
			if _, err := io.WriteString(writer, ","); err != nil {
				return err
			}
		}
		if _, err := writer.Write(encoded); err != nil {
			return err
		}
		if canonical != nil {
			classKey := string(encoded)
			canonical.classByCPU[cpu] = classKey
			canonical.members[classKey] = append(canonical.members[classKey], cpu)
		}
	}
	_, err := io.WriteString(writer, "]}")
	return err
}

func newPartitionResidualDigest(
	demands []partitionDemand,
	topology *machine.CPUTopology,
) (partitionResidualDigest, error) {
	hasher := sha256.New()
	if err := writePartitionResidualCanonical(hasher, demands, topology, nil); err != nil {
		return partitionResidualDigest{}, err
	}
	var digest partitionResidualDigest
	copy(digest[:], hasher.Sum(nil))
	return digest, nil
}

func newCanonicalPartitionResidual(
	demands []partitionDemand,
	topology *machine.CPUTopology,
) (partitionResidualCanonical, error) {
	return newCanonicalPartitionResidualWithLimit(
		demands, topology, int(^uint(0)>>1))
}

func newCanonicalPartitionResidualWithLimit(
	demands []partitionDemand,
	topology *machine.CPUTopology,
	maxPayloadBytes int,
) (partitionResidualCanonical, error) {
	canonical := partitionResidualCanonical{
		classByCPU: make(map[int]string),
		members:    make(map[string][]int),
	}
	payload := partitionResidualBoundedBuffer{limit: maxPayloadBytes}
	hasher := sha256.New()
	if err := writePartitionResidualCanonical(
		io.MultiWriter(&payload, hasher), demands, topology, &canonical); err != nil {
		return partitionResidualCanonical{}, err
	}
	copy(canonical.digest[:], hasher.Sum(nil))
	canonical.payload = payload.String()
	return canonical, nil
}

func canonicalizePartitionAssignments(
	assignments map[string]machine.CPUSet,
	canonical partitionResidualCanonical,
) (map[string]map[string]int, error) {
	result := make(map[string]map[string]int, len(assignments))
	for demandKey, cpus := range assignments {
		counts := make(map[string]int)
		for _, cpu := range cpus.ToSliceInt() {
			classKey, found := canonical.classByCPU[cpu]
			if !found {
				return nil, fmt.Errorf("partition assignment CPU %d has no canonical class", cpu)
			}
			counts[classKey]++
		}
		result[demandKey] = counts
	}
	return result, nil
}

func remapPartitionAssignments(
	counts map[string]map[string]int,
	canonical partitionResidualCanonical,
) (map[string]machine.CPUSet, error) {
	result := make(map[string]machine.CPUSet, len(counts))
	nextByClass := make(map[string]int, len(canonical.members))
	demandKeys := make([]string, 0, len(counts))
	for demandKey := range counts {
		demandKeys = append(demandKeys, demandKey)
	}
	sort.Strings(demandKeys)
	for _, demandKey := range demandKeys {
		result[demandKey] = machine.NewCPUSet()
		classKeys := make([]string, 0, len(counts[demandKey]))
		for classKey := range counts[demandKey] {
			classKeys = append(classKeys, classKey)
		}
		sort.Strings(classKeys)
		for _, classKey := range classKeys {
			start := nextByClass[classKey]
			end := start + counts[demandKey][classKey]
			members := canonical.members[classKey]
			if end > len(members) {
				return nil, fmt.Errorf("canonical partition class has %d CPUs, need %d",
					len(members), end)
			}
			result[demandKey].Add(members[start:end]...)
			nextByClass[classKey] = end
		}
	}
	return result, nil
}

func preparePartitionResidualCache(
	cache *partitionResidualSolveCache,
	demandSets [][]partitionDemand,
) error {
	return preparePartitionResidualCacheLazy(cache, len(demandSets), func(i int) []partitionDemand {
		return demandSets[i]
	})
}

func preparePartitionResidualCacheLazy(
	cache *partitionResidualSolveCache,
	count int,
	demandsAt func(int) []partitionDemand,
) error {
	cache.entries = make(map[partitionResidualDigest][]partitionResidualSolveCacheEntry)
	cache.repeatedDigests = make(map[partitionResidualDigest]struct{})
	cache.repeatedPayloads = make(map[partitionResidualDigest]map[string]*partitionResidualPreparedPayload)
	cache.usage = partitionResidualCacheUsage{preparationGraphs: count}
	if count < 0 || count > cache.limits.maxPreparationGraphs {
		return partitionResidualLimitError(
			errPartitionResidualPreparationWorkBudget, "graphs",
			count, cache.limits.maxPreparationGraphs)
	}

	digestCounts := make(map[partitionResidualDigest]int, count)
	digests := make([]partitionResidualDigest, count)
	for i := 0; i < count; i++ {
		demands := demandsAt(i)
		if err := cache.consumeCanonicalWork(demands); err != nil {
			return err
		}
		digest, err := newPartitionResidualDigest(demands, cache.topology)
		if err != nil {
			return err
		}
		digests[i] = digest
		digestCounts[digest]++
	}

	payloadCounts := make(map[partitionResidualDigest]map[string]int)
	for i := 0; i < count; i++ {
		digest := digests[i]
		if digestCounts[digest] < 2 {
			continue
		}
		demands := demandsAt(i)
		if err := cache.consumeCanonicalWork(demands); err != nil {
			return err
		}
		// A canonical payload is retained only when it is an exact repeat, so
		// its only meaningful size bound is the cache's total retained-byte
		// budget. A smaller per-payload cap can reject a graph that the solver
		// budgets otherwise admit.
		canonical, err := newCanonicalPartitionResidualWithLimit(
			demands, cache.topology, cache.limits.maxRetainedBytes)
		if err != nil {
			return err
		}
		if payloadCounts[digest] == nil {
			payloadCounts[digest] = make(map[string]int)
		}
		if _, found := payloadCounts[digest][canonical.payload]; !found {
			next := cache.usage.retainedBytes + len(canonical.payload)
			if next > cache.limits.maxRetainedBytes {
				return partitionResidualLimitError(
					errPartitionResidualRetainedMemoryBudget,
					"canonical payload bytes", next, cache.limits.maxRetainedBytes)
			}
			cache.usage.retainedBytes = next
		}
		payloadCounts[digest][canonical.payload]++
	}
	repeatedDigests := make(map[partitionResidualDigest]struct{})
	repeatedPayloads := make(map[partitionResidualDigest]map[string]*partitionResidualPreparedPayload)
	retainedBytes := 0
	entries := 0
	digestKeys := make([]partitionResidualDigest, 0, len(payloadCounts))
	for digest := range payloadCounts {
		digestKeys = append(digestKeys, digest)
	}
	sort.Slice(digestKeys, func(i, j int) bool {
		return bytes.Compare(digestKeys[i][:], digestKeys[j][:]) < 0
	})
	for _, digest := range digestKeys {
		counts := payloadCounts[digest]
		payloads := make([]string, 0, len(counts))
		for payload := range counts {
			payloads = append(payloads, payload)
		}
		sort.Strings(payloads)
		for _, payload := range payloads {
			count := counts[payload]
			if count < 2 {
				continue
			}
			entries++
			if entries > cache.limits.maxEntries {
				return partitionResidualLimitError(
					errPartitionResidualRetainedMemoryBudget,
					"canonical entries", entries, cache.limits.maxEntries)
			}
			retainedBytes += len(payload)
			if retainedBytes > cache.limits.maxRetainedBytes {
				return partitionResidualLimitError(
					errPartitionResidualRetainedMemoryBudget,
					"canonical payload bytes", retainedBytes, cache.limits.maxRetainedBytes)
			}
			repeatedDigests[digest] = struct{}{}
			if repeatedPayloads[digest] == nil {
				repeatedPayloads[digest] = make(map[string]*partitionResidualPreparedPayload)
			}
			repeatedPayloads[digest][payload] = &partitionResidualPreparedPayload{
				payload: payload,
			}
		}
	}
	cache.repeatedDigests = repeatedDigests
	cache.repeatedPayloads = repeatedPayloads
	cache.usage.retainedBytes = retainedBytes
	return nil
}

func partitionResidualLimitError(
	kind error,
	resource string,
	used, limit int,
) error {
	return &partitionResidualCacheLimitError{
		kind: kind, resource: resource, used: used, limit: limit,
	}
}

func (c *partitionResidualSolveCache) consumeCanonicalWork(
	demands []partitionDemand,
) error {
	allEligible := machine.NewCPUSet()
	for _, demand := range demands {
		allEligible = allEligible.Union(demand.eligible)
	}
	work := len(demands)
	if len(demands) > 0 && allEligible.Size() > (int(^uint(0)>>1)-work)/len(demands) {
		return partitionResidualLimitError(
			errPartitionResidualPreparationWorkBudget,
			"canonical work units", int(^uint(0)>>1), c.limits.maxCanonicalWork)
	}
	work += allEligible.Size() * len(demands)
	if work > c.limits.maxCanonicalWork-c.usage.canonicalWork {
		return partitionResidualLimitError(
			errPartitionResidualPreparationWorkBudget,
			"canonical work units", c.usage.canonicalWork+work, c.limits.maxCanonicalWork)
	}
	c.usage.canonicalWork += work
	return nil
}

func (c *partitionResidualSolveCache) preparedPayload(
	canonical partitionResidualCanonical,
) (*partitionResidualPreparedPayload, bool) {
	if _, found := c.repeatedDigests[canonical.digest]; !found {
		return nil, false
	}
	repeatedPayloads, exactPrepared := c.repeatedPayloads[canonical.digest]
	if !exactPrepared {
		return nil, true
	}
	prepared, found := repeatedPayloads[canonical.payload]
	return prepared, found
}

func (c *partitionResidualSolveCache) lookupCanonical(
	canonical partitionResidualCanonical,
) (partitionResidualSolveResult, bool, error) {
	prepared, shouldCache := c.preparedPayload(canonical)
	if !shouldCache {
		return partitionResidualSolveResult{}, false, nil
	}
	var result partitionResidualSolveResult
	found := false
	for _, entry := range c.entries[canonical.digest] {
		if (prepared != nil && entry.prepared == prepared) ||
			(prepared == nil && entry.prepared.payload == canonical.payload) {
			result = entry.result
			found = true
			break
		}
	}
	if !found {
		c.misses++
		return partitionResidualSolveResult{}, false, nil
	}
	c.hits++
	if result.infeasible {
		return result, true, nil
	}
	assignments, err := remapPartitionAssignments(
		result.canonicalAssignments, canonical)
	if err != nil {
		return partitionResidualSolveResult{}, false, err
	}
	return partitionResidualSolveResult{assignments: assignments}, true, nil
}

func (c *partitionResidualSolveCache) storeCanonicalSuccess(
	canonical partitionResidualCanonical,
	assignments map[string]machine.CPUSet,
) error {
	counts, err := canonicalizePartitionAssignments(assignments, canonical)
	if err != nil {
		return err
	}
	prepared, shouldCache := c.preparedPayload(canonical)
	if !shouldCache {
		return nil
	}
	result := partitionResidualSolveResult{canonicalAssignments: counts}
	bucket := c.entries[canonical.digest]
	for i := range bucket {
		if (prepared != nil && bucket[i].prepared == prepared) ||
			(prepared == nil && bucket[i].prepared.payload == canonical.payload) {
			bucket[i].result = result
			c.entries[canonical.digest] = bucket
			return nil
		}
	}
	if err := c.reserveResult(canonical, result, prepared != nil); err != nil {
		return err
	}
	if prepared == nil {
		prepared = &partitionResidualPreparedPayload{payload: canonical.payload}
	}
	c.entries[canonical.digest] = append(bucket, partitionResidualSolveCacheEntry{
		prepared: prepared,
		result:   result,
	})
	return nil
}

func (c *partitionResidualSolveCache) storeCanonicalInfeasible(
	canonical partitionResidualCanonical,
) error {
	prepared, shouldCache := c.preparedPayload(canonical)
	if !shouldCache {
		return nil
	}
	result := partitionResidualSolveResult{infeasible: true}
	bucket := c.entries[canonical.digest]
	for i := range bucket {
		if (prepared != nil && bucket[i].prepared == prepared) ||
			(prepared == nil && bucket[i].prepared.payload == canonical.payload) {
			bucket[i].result = result
			c.entries[canonical.digest] = bucket
			return nil
		}
	}
	if err := c.reserveResult(canonical, result, prepared != nil); err != nil {
		return err
	}
	if prepared == nil {
		prepared = &partitionResidualPreparedPayload{payload: canonical.payload}
	}
	c.entries[canonical.digest] = append(bucket, partitionResidualSolveCacheEntry{
		prepared: prepared,
		result:   result,
	})
	return nil
}

func (c *partitionResidualSolveCache) reserveResult(
	canonical partitionResidualCanonical,
	result partitionResidualSolveResult,
	payloadAlreadyRetained bool,
) error {
	units, retainedBytes := 1, 0
	for demandKey, counts := range result.canonicalAssignments {
		units++
		retainedBytes += len(demandKey)
		for classKey := range counts {
			units++
			retainedBytes += len(classKey)
		}
	}
	if c.usage.entries+1 > c.limits.maxEntries {
		return partitionResidualLimitError(
			errPartitionResidualRetainedMemoryBudget,
			"result entries", c.usage.entries+1, c.limits.maxEntries)
	}
	if c.usage.resultUnits+units > c.limits.maxResultUnits {
		return partitionResidualLimitError(
			errPartitionResidualRetainedMemoryBudget,
			"result units", c.usage.resultUnits+units, c.limits.maxResultUnits)
	}
	if !payloadAlreadyRetained {
		retainedBytes += len(canonical.payload)
	}
	if c.usage.retainedBytes+retainedBytes > c.limits.maxRetainedBytes {
		return partitionResidualLimitError(
			errPartitionResidualRetainedMemoryBudget,
			"payload/result bytes", c.usage.retainedBytes+retainedBytes,
			c.limits.maxRetainedBytes)
	}
	c.usage.entries++
	c.usage.resultUnits += units
	c.usage.retainedBytes += retainedBytes
	return nil
}

func solvePartitionResidualCached(
	demands []partitionDemand,
	cache *partitionResidualSolveCache,
	graphBudget partitionGraphBudget,
	searchBudget *partitionSearchBudget,
) (map[string]machine.CPUSet, bool, partitionGraphBudget, error) {
	digest, err := newPartitionResidualDigest(demands, cache.topology)
	if err != nil {
		return nil, false, graphBudget, err
	}
	if _, repeated := cache.repeatedDigests[digest]; !repeated {
		assignments, solveErr := solveDisjointPartitionsWithBudgets(
			demands, cache.topology, &graphBudget, searchBudget)
		return assignments, false, graphBudget, solveErr
	}
	canonical, err := newCanonicalPartitionResidualWithLimit(
		demands, cache.topology, cache.limits.maxRetainedBytes)
	if err != nil {
		return nil, false, graphBudget, err
	}
	cached, found, err := cache.lookupCanonical(canonical)
	if err != nil {
		return nil, false, graphBudget, err
	}
	if found {
		if cached.infeasible {
			return nil, true, graphBudget, errPartitionNoFeasibleAssignment
		}
		return cached.assignments, true, graphBudget, nil
	}

	assignments, err := solveDisjointPartitionsWithBudgets(
		demands, cache.topology, &graphBudget, searchBudget)
	if err == nil {
		if storeErr := cache.storeCanonicalSuccess(canonical, assignments); storeErr != nil {
			return nil, false, graphBudget, storeErr
		}
	} else if errors.Is(err, errPartitionNoFeasibleAssignment) {
		if storeErr := cache.storeCanonicalInfeasible(canonical); storeErr != nil {
			return nil, false, graphBudget, storeErr
		}
	}
	return assignments, false, graphBudget, err
}
