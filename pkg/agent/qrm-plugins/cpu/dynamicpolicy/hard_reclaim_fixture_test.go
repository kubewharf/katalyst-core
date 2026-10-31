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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type hardReclaimFixtureCPU struct {
	ID         int `json:"id"`
	NUMANodeID int `json:"numaNodeID"`
	SocketID   int `json:"socketID"`
	CoreID     int `json:"coreID"`
}

type hardReclaimFixtureDemand struct {
	Key             string  `json:"key"`
	RequestGroupKey string  `json:"requestGroupKey"`
	Quantity        int     `json:"quantity"`
	RequestQuantity float64 `json:"requestQuantity"`
	Eligible        string  `json:"eligible"`
	Preferred       string  `json:"preferred"`
	Class           string  `json:"class"`
}

type hardReclaimGeneratedTopology struct {
	NumCPUs    int `json:"numCPUs"`
	NumSockets int `json:"numSockets"`
	NumNUMAs   int `json:"numNUMAs"`
}

type hardReclaimFixture struct {
	Name                     string                        `json:"name"`
	GeneratedTopology        *hardReclaimGeneratedTopology `json:"generatedTopology,omitempty"`
	CPUs                     []hardReclaimFixtureCPU       `json:"cpus"`
	Demands                  []hardReclaimFixtureDemand    `json:"demands"`
	Available                string                        `json:"available"`
	ExpectedReclaim          string                        `json:"expectedReclaim"`
	ExpectedAssignmentSizes  map[string]int                `json:"expectedAssignmentSizes"`
	ExpectCompleteNoFeasible bool                          `json:"expectCompleteNoFeasible"`
}

func TestParseHardReclaimFixtureRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	const (
		cpu    = `{"id":0,"numaNodeID":0,"socketID":0,"coreID":0}`
		cpu1   = `{"id":1,"numaNodeID":0,"socketID":0,"coreID":1}`
		demand = `{"key":"reclaim","requestGroupKey":"","quantity":1,` +
			`"requestQuantity":0,"eligible":"0","preferred":"0",` +
			`"class":"mandatory-reclaim"}`
		valid = `{"name":"fixture","cpus":[` + cpu + `],"demands":[` + demand + `],` +
			`"available":"0","expectedReclaim":"0",` +
			`"expectedAssignmentSizes":{"reclaim":1},` +
			`"expectCompleteNoFeasible":false}`
	)

	replace := func(old, replacement string) string {
		return strings.Replace(valid, old, replacement, 1)
	}
	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{
			name:    "unknown JSON field",
			data:    replace(`"name":"fixture"`, `"name":"fixture","unknown":true`),
			wantErr: `json: unknown field "unknown"`,
		},
		{
			name:    "duplicate top-level key",
			data:    replace(`"name":"fixture"`, `"name":"fixture","name":"fixture"`),
			wantErr: `duplicate JSON key "name"`,
		},
		{
			name: "duplicate generated topology key",
			data: replace(`"cpus":[`+cpu+`]`,
				`"generatedTopology":{"numCPUs":4,"numCPUs":4,"numSockets":1,"numNUMAs":1}`),
			wantErr: `duplicate JSON key "numCPUs"`,
		},
		{
			name: "duplicate explicit topology key",
			data: replace(cpu,
				`{"id":0,"id":0,"numaNodeID":0,"socketID":0,"coreID":0}`),
			wantErr: `duplicate JSON key "id"`,
		},
		{
			name:    "duplicate demand key field",
			data:    replace(`"key":"reclaim"`, `"key":"reclaim","key":"reclaim"`),
			wantErr: `duplicate JSON key "key"`,
		},
		{
			name: "duplicate expected assignment key",
			data: replace(`"expectedAssignmentSizes":{"reclaim":1}`,
				`"expectedAssignmentSizes":{"reclaim":1,"reclaim":1}`),
			wantErr: `duplicate JSON key "reclaim"`,
		},
		{
			name: "unknown generated topology field",
			data: replace(`"cpus":[`+cpu+`]`,
				`"generatedTopology":{"numCPUs":1,"numSockets":1,"numNUMAs":1,"unknown":true}`),
			wantErr: `json: unknown field "unknown"`,
		},
		{
			name: "unknown explicit topology field",
			data: replace(cpu,
				`{"id":0,"numaNodeID":0,"socketID":0,"coreID":0,"unknown":true}`),
			wantErr: `json: unknown field "unknown"`,
		},
		{
			name: "unknown demand field",
			data: replace(`"class":"mandatory-reclaim"`,
				`"class":"mandatory-reclaim","unknown":true`),
			wantErr: `json: unknown field "unknown"`,
		},
		{
			name:    "trailing JSON value",
			data:    valid + `{}`,
			wantErr: "trailing content",
		},
		{
			name:    "fixture name mismatch",
			data:    replace(`"name":"fixture"`, `"name":"other"`),
			wantErr: `fixture name "other" does not match file name "fixture"`,
		},
		{
			name:    "missing topology representation",
			data:    replace(`"cpus":[`+cpu+`]`, `"cpus":[]`),
			wantErr: "must set exactly one topology representation",
		},
		{
			name: "both topology representations",
			data: replace(`"cpus":[`+cpu+`]`,
				`"generatedTopology":{"numCPUs":1,"numSockets":1,"numNUMAs":1},`+
					`"cpus":[`+cpu+`]`),
			wantErr: "must set exactly one topology representation",
		},
		{
			name: "non-positive generated CPU count",
			data: replace(`"cpus":[`+cpu+`]`,
				`"generatedTopology":{"numCPUs":0,"numSockets":1,"numNUMAs":1}`),
			wantErr: "non-positive generated topology parameters",
		},
		{
			name: "non-positive generated socket count",
			data: replace(`"cpus":[`+cpu+`]`,
				`"generatedTopology":{"numCPUs":1,"numSockets":0,"numNUMAs":1}`),
			wantErr: "non-positive generated topology parameters",
		},
		{
			name: "non-positive generated NUMA count",
			data: replace(`"cpus":[`+cpu+`]`,
				`"generatedTopology":{"numCPUs":1,"numSockets":1,"numNUMAs":0}`),
			wantErr: "non-positive generated topology parameters",
		},
		{
			name: "generated topology rejected by generator",
			data: replace(`"cpus":[`+cpu+`]`,
				`"generatedTopology":{"numCPUs":1,"numSockets":2,"numNUMAs":1}`),
			wantErr: "generated topology:",
		},
		{
			name:    "negative explicit CPU ID",
			data:    replace(`"id":0`, `"id":-1`),
			wantErr: "negative CPU topology parameter",
		},
		{
			name:    "negative explicit NUMA ID",
			data:    replace(`"numaNodeID":0`, `"numaNodeID":-1`),
			wantErr: "negative CPU topology parameter",
		},
		{
			name:    "negative explicit socket ID",
			data:    replace(`"socketID":0`, `"socketID":-1`),
			wantErr: "negative CPU topology parameter",
		},
		{
			name:    "negative explicit core ID",
			data:    replace(`"coreID":0`, `"coreID":-1`),
			wantErr: "negative CPU topology parameter",
		},
		{
			name: "duplicate CPU ID",
			data: replace(`"cpus":[`+cpu+`]`,
				`"cpus":[`+cpu+`,{"id":0,"numaNodeID":0,"socketID":0,"coreID":1}]`),
			wantErr: "duplicate CPU ID 0",
		},
		{
			name:    "empty demand key",
			data:    replace(`"key":"reclaim"`, `"key":""`),
			wantErr: "has empty demand key",
		},
		{
			name:    "duplicate demand key",
			data:    replace(`"demands":[`+demand+`]`, `"demands":[`+demand+`,`+demand+`]`),
			wantErr: `has duplicate demand key "reclaim"`,
		},
		{
			name: "empty demands",
			data: strings.Replace(
				strings.Replace(
					replace(`"demands":[`+demand+`]`, `"demands":[]`),
					`"expectedReclaim":"0"`, `"expectedReclaim":""`, 1),
				`"expectedAssignmentSizes":{"reclaim":1}`,
				`"expectedAssignmentSizes":{}`, 1),
			wantErr: "has no demands",
		},
		{
			name:    "non-positive quantity",
			data:    replace(`"quantity":1`, `"quantity":0`),
			wantErr: `demand "reclaim" has non-positive quantity 0`,
		},
		{
			name:    "negative request quantity",
			data:    replace(`"requestQuantity":0`, `"requestQuantity":-0.5`),
			wantErr: `demand "reclaim" has negative request quantity -0.5`,
		},
		{
			name:    "invalid class",
			data:    replace(`"class":"mandatory-reclaim"`, `"class":"unknown"`),
			wantErr: `demand "reclaim" has invalid class "unknown"`,
		},
		{
			name:    "malformed eligible CPU set",
			data:    replace(`"eligible":"0"`, `"eligible":"bad"`),
			wantErr: `demand "reclaim" eligible CPUs`,
		},
		{
			name:    "eligible CPU outside topology",
			data:    replace(`"eligible":"0"`, `"eligible":"0-1"`),
			wantErr: `demand "reclaim" eligible CPUs are outside topology`,
		},
		{
			name:    "malformed preferred CPU set",
			data:    replace(`"preferred":"0"`, `"preferred":"bad"`),
			wantErr: `demand "reclaim" preferred CPUs`,
		},
		{
			name:    "preferred CPU outside topology",
			data:    replace(`"preferred":"0"`, `"preferred":"1"`),
			wantErr: `demand "reclaim" preferred CPUs are outside topology`,
		},
		{
			name: "preferred CPU outside eligible CPUs",
			data: strings.Replace(
				replace(`"cpus":[`+cpu+`]`, `"cpus":[`+cpu+`,`+cpu1+`]`),
				`"preferred":"0"`, `"preferred":"1"`, 1),
			wantErr: `demand "reclaim" preferred CPUs are outside eligible CPUs`,
		},
		{
			name:    "malformed available CPU set",
			data:    replace(`"available":"0"`, `"available":"bad"`),
			wantErr: "available CPUs",
		},
		{
			name:    "available CPU outside topology",
			data:    replace(`"available":"0"`, `"available":"0-1"`),
			wantErr: "available CPUs are outside topology",
		},
		{
			name: "expected assignment missing",
			data: replace(`"expectedAssignmentSizes":{"reclaim":1}`,
				`"expectedAssignmentSizes":{}`),
			wantErr: `is missing expected assignment "reclaim"`,
		},
		{
			name: "expected assignment extra",
			data: replace(`"expectedAssignmentSizes":{"reclaim":1}`,
				`"expectedAssignmentSizes":{"reclaim":1,"extra":1}`),
			wantErr: `has unexpected assignment key "extra"`,
		},
		{
			name: "expected assignment non-positive",
			data: replace(`"expectedAssignmentSizes":{"reclaim":1}`,
				`"expectedAssignmentSizes":{"reclaim":0}`),
			wantErr: `assignment "reclaim" has size 0, want 1`,
		},
		{
			name: "expected assignment total mismatch",
			data: replace(`"expectedAssignmentSizes":{"reclaim":1}`,
				`"expectedAssignmentSizes":{"reclaim":2}`),
			wantErr: `assignment "reclaim" has size 2, want 1`,
		},
		{
			name: "expected assignment per-key mismatch",
			data: `{"name":"fixture","cpus":[` + cpu + `,` + cpu1 +
				`,{"id":2,"numaNodeID":0,"socketID":0,"coreID":2}],` +
				`"demands":[` +
				`{"key":"reclaim","requestGroupKey":"","quantity":1,"requestQuantity":0,` +
				`"eligible":"0-2","preferred":"0","class":"mandatory-reclaim"},` +
				`{"key":"dedicated","requestGroupKey":"","quantity":2,"requestQuantity":2,` +
				`"eligible":"0-2","preferred":"1-2","class":"dedicated"}],` +
				`"available":"0-2","expectedReclaim":"0",` +
				`"expectedAssignmentSizes":{"reclaim":2,"dedicated":1},` +
				`"expectCompleteNoFeasible":false}`,
			wantErr: `assignment "reclaim" has size 2, want 1`,
		},
		{
			name: "demand total exceeds available CPUs",
			data: `{"name":"fixture","cpus":[` + cpu + `],"demands":[` +
				demand + `,{"key":"dedicated","requestGroupKey":"","quantity":1,` +
				`"requestQuantity":1,"eligible":"0","preferred":"",` +
				`"class":"dedicated"}],"available":"0","expectedReclaim":"0",` +
				`"expectedAssignmentSizes":{"reclaim":1,"dedicated":1},` +
				`"expectCompleteNoFeasible":false}`,
			wantErr: "demand total 2 exceeds available CPU count 1",
		},
		{
			name: "demand quantity exceeds eligible CPUs",
			data: strings.Replace(
				strings.Replace(
					replace(`"cpus":[`+cpu+`]`, `"cpus":[`+cpu+`,`+cpu1+`]`),
					`"quantity":1`, `"quantity":2`, 1),
				`"expectedAssignmentSizes":{"reclaim":1}`,
				`"expectedAssignmentSizes":{"reclaim":2}`, 1),
			wantErr: `demand "reclaim" quantity 2 exceeds eligible available CPU count 1`,
		},
		{
			name: "demand eligible CPUs outside available CPUs",
			data: strings.Replace(
				replace(`"cpus":[`+cpu+`]`, `"cpus":[`+cpu+`,`+cpu1+`]`),
				`"eligible":"0"`, `"eligible":"0-1"`, 1),
			wantErr: `demand "reclaim" eligible CPUs are outside available CPUs`,
		},
		{
			name:    "malformed expected reclaim CPU set",
			data:    replace(`"expectedReclaim":"0"`, `"expectedReclaim":"bad"`),
			wantErr: "expected reclaim CPUs",
		},
		{
			name:    "expected reclaim CPU outside topology",
			data:    replace(`"expectedReclaim":"0"`, `"expectedReclaim":"1"`),
			wantErr: "expected reclaim CPUs are outside topology",
		},
		{
			name: "expected reclaim CPU outside available CPUs",
			data: strings.Replace(
				replace(`"cpus":[`+cpu+`]`, `"cpus":[`+cpu+`,`+cpu1+`]`),
				`"expectedReclaim":"0"`, `"expectedReclaim":"1"`, 1),
			wantErr: "expected reclaim CPUs are outside available CPUs",
		},
		{
			name: "expected reclaim CPU outside mandatory eligibility",
			data: strings.Replace(
				strings.Replace(
					replace(`"cpus":[`+cpu+`]`, `"cpus":[`+cpu+`,`+cpu1+`]`),
					`"available":"0"`, `"available":"0-1"`, 1),
				`"expectedReclaim":"0"`, `"expectedReclaim":"1"`, 1),
			wantErr: "expected reclaim CPUs are outside mandatory eligibility",
		},
		{
			name:    "expected reclaim size mismatches mandatory demand total",
			data:    replace(`"expectedReclaim":"0"`, `"expectedReclaim":""`),
			wantErr: "expected reclaim size 0 does not match mandatory reclaim total 1",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, _, _, err := parseHardReclaimFixture("fixture", []byte(tt.data))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestParseHardReclaimFixtureAcceptsConsistentExplicitTopology(t *testing.T) {
	t.Parallel()

	data := []byte(`{
		"name":"explicit",
		"cpus":[
			{"id":0,"numaNodeID":0,"socketID":0,"coreID":0},
			{"id":1,"numaNodeID":0,"socketID":0,"coreID":1},
			{"id":2,"numaNodeID":1,"socketID":1,"coreID":0},
			{"id":3,"numaNodeID":1,"socketID":1,"coreID":1}
		],
		"demands":[
			{"key":"reclaim","requestGroupKey":"","quantity":2,"requestQuantity":0,
			 "eligible":"0-1","preferred":"0-1","class":"mandatory-reclaim"},
			{"key":"dedicated","requestGroupKey":"pod/main","quantity":2,"requestQuantity":2,
			 "eligible":"2-3","preferred":"2-3","class":"dedicated"}
		],
		"available":"0-3",
		"expectedReclaim":"0-1",
		"expectedAssignmentSizes":{"reclaim":2,"dedicated":2},
		"expectCompleteNoFeasible":false
	}`)

	_, topology, demands, available, err := parseHardReclaimFixture("explicit", data)

	require.NoError(t, err)
	require.Equal(t, 4, topology.NumCPUs)
	require.Equal(t, 4, topology.NumCores)
	require.Equal(t, 2, topology.NumSockets)
	require.Equal(t, 2, topology.NumNUMANodes)
	require.Len(t, demands, 2)
	require.Equal(t, machine.NewCPUSet(0, 1, 2, 3), available)
}

func TestParseHardReclaimFixtureRejectsInconsistentExplicitTopology(t *testing.T) {
	t.Parallel()

	const valid = `{
		"name":"explicit",
		"cpus":[
			{"id":0,"numaNodeID":0,"socketID":0,"coreID":0},
			{"id":1,"numaNodeID":0,"socketID":0,"coreID":0}
		],
		"demands":[
			{"key":"reclaim","requestGroupKey":"","quantity":2,"requestQuantity":0,
			 "eligible":"0-1","preferred":"0-1","class":"mandatory-reclaim"}
		],
		"available":"0-1",
		"expectedReclaim":"0-1",
		"expectedAssignmentSizes":{"reclaim":2},
		"expectCompleteNoFeasible":false
	}`
	tests := []struct {
		name    string
		data    string
		wantErr string
	}{
		{
			name: "core spans NUMA nodes",
			data: strings.Replace(valid,
				`{"id":1,"numaNodeID":0,"socketID":0,"coreID":0}`,
				`{"id":1,"numaNodeID":1,"socketID":0,"coreID":0}`, 1),
			wantErr: "core socket=0 core=0 spans NUMA nodes",
		},
		{
			name: "NUMA node spans sockets",
			data: strings.Replace(valid,
				`{"id":1,"numaNodeID":0,"socketID":0,"coreID":0}`,
				`{"id":1,"numaNodeID":0,"socketID":1,"coreID":1}`, 1),
			wantErr: "NUMA node 0 spans sockets",
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, _, _, err := parseHardReclaimFixture("explicit", []byte(tt.data))
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func parseHardReclaimFixture(name string, data []byte) (
	hardReclaimFixture,
	*machine.CPUTopology,
	[]partitionDemand,
	machine.CPUSet,
	error,
) {
	var fixture hardReclaimFixture
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("decode fixture %q: %w", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("decode fixture %q: %w", name, err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("decode fixture %q trailing content: %w", name, err)
	}
	if fixture.Name != name {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("fixture name %q does not match file name %q", fixture.Name, name)
	}
	requireFeasibleExpectations := !fixture.ExpectCompleteNoFeasible

	topology, err := hardReclaimFixtureTopology(fixture)
	if err != nil {
		return fixture, nil, nil, machine.CPUSet{}, err
	}
	topologyCPUs := topology.CPUDetails.CPUs()
	available, err := machine.Parse(fixture.Available)
	if err != nil {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("fixture %q available CPUs: %w", name, err)
	}
	if !available.IsSubsetOf(topologyCPUs) {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("fixture %q available CPUs are outside topology", name)
	}

	demands := make([]partitionDemand, 0, len(fixture.Demands))
	if len(fixture.Demands) == 0 {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("fixture %q has no demands", name)
	}
	demandQuantities := make(map[string]int, len(fixture.Demands))
	totalQuantity := 0
	mandatoryReclaimQuantity := 0
	mandatoryReclaimEligible := machine.NewCPUSet()
	for _, item := range fixture.Demands {
		if strings.TrimSpace(item.Key) == "" {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q has empty demand key", name)
		}
		if _, exists := demandQuantities[item.Key]; exists {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q has duplicate demand key %q", name, item.Key)
		}
		if item.Quantity <= 0 {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q demand %q has non-positive quantity %d",
					name, item.Key, item.Quantity)
		}
		if item.RequestQuantity < 0 {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q demand %q has negative request quantity %v",
					name, item.Key, item.RequestQuantity)
		}
		class := advisorBlockClass(item.Class)
		switch class {
		case advisorBlockClassMandatoryReclaim,
			advisorBlockClassDedicated,
			advisorBlockClassShared:
		default:
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q demand %q has invalid class %q",
					name, item.Key, item.Class)
		}

		eligible, parseErr := machine.Parse(item.Eligible)
		if parseErr != nil {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q demand %q eligible CPUs: %w",
					name, item.Key, parseErr)
		}
		preferred, parseErr := machine.Parse(item.Preferred)
		if parseErr != nil {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q demand %q preferred CPUs: %w",
					name, item.Key, parseErr)
		}
		if !eligible.IsSubsetOf(topologyCPUs) {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q demand %q eligible CPUs are outside topology",
					name, item.Key)
		}
		if !preferred.IsSubsetOf(topologyCPUs) {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q demand %q preferred CPUs are outside topology",
					name, item.Key)
		}
		if !preferred.IsSubsetOf(eligible) {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q demand %q preferred CPUs are outside eligible CPUs",
					name, item.Key)
		}
		if requireFeasibleExpectations && !eligible.IsSubsetOf(available) {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q demand %q eligible CPUs are outside available CPUs",
					name, item.Key)
		}
		availableEligible := eligible.Intersection(available)
		if requireFeasibleExpectations && item.Quantity > availableEligible.Size() {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf(
					"fixture %q demand %q quantity %d exceeds eligible available CPU count %d",
					name, item.Key, item.Quantity, availableEligible.Size())
		}

		demandQuantities[item.Key] = item.Quantity
		totalQuantity += item.Quantity
		if class == advisorBlockClassMandatoryReclaim {
			mandatoryReclaimQuantity += item.Quantity
			mandatoryReclaimEligible = mandatoryReclaimEligible.Union(eligible)
		}
		demands = append(demands, partitionDemand{
			key: item.Key, requestGroupKey: item.RequestGroupKey,
			quantity: item.Quantity, requestQuantity: item.RequestQuantity,
			eligible: eligible, preferred: preferred, class: class,
		})
	}

	for key := range fixture.ExpectedAssignmentSizes {
		_, exists := demandQuantities[key]
		if !exists {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q has unexpected assignment key %q", name, key)
		}
	}
	for _, item := range fixture.Demands {
		size, exists := fixture.ExpectedAssignmentSizes[item.Key]
		if !exists {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q is missing expected assignment %q", name, item.Key)
		}
		if requireFeasibleExpectations && size != item.Quantity {
			return fixture, nil, nil, machine.CPUSet{},
				fmt.Errorf("fixture %q assignment %q has size %d, want %d",
					name, item.Key, size, item.Quantity)
		}
	}
	if requireFeasibleExpectations && totalQuantity > available.Size() {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("fixture %q demand total %d exceeds available CPU count %d",
				name, totalQuantity, available.Size())
	}

	expectedReclaim, err := machine.Parse(fixture.ExpectedReclaim)
	if err != nil {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("fixture %q expected reclaim CPUs: %w", name, err)
	}
	if !expectedReclaim.IsSubsetOf(topologyCPUs) {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("fixture %q expected reclaim CPUs are outside topology", name)
	}
	if requireFeasibleExpectations && !expectedReclaim.IsSubsetOf(available) {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("fixture %q expected reclaim CPUs are outside available CPUs", name)
	}
	if requireFeasibleExpectations && !expectedReclaim.IsSubsetOf(mandatoryReclaimEligible) {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("fixture %q expected reclaim CPUs are outside mandatory eligibility", name)
	}
	if requireFeasibleExpectations && expectedReclaim.Size() != mandatoryReclaimQuantity {
		return fixture, nil, nil, machine.CPUSet{},
			fmt.Errorf("fixture %q expected reclaim size %d does not match mandatory reclaim total %d",
				name, expectedReclaim.Size(), mandatoryReclaimQuantity)
	}
	return fixture, topology, demands, available, nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			keys := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return fmt.Errorf("object key is not a string")
				}
				if _, found := keys[key]; found {
					return fmt.Errorf("duplicate JSON key %q", key)
				}
				keys[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err = decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delim)
		}
	}
	return walk()
}

func hardReclaimFixtureTopology(
	fixture hardReclaimFixture,
) (*machine.CPUTopology, error) {
	if (fixture.GeneratedTopology != nil) == (len(fixture.CPUs) > 0) {
		return nil, fmt.Errorf("fixture %q must set exactly one topology representation",
			fixture.Name)
	}
	if fixture.GeneratedTopology != nil {
		generated := fixture.GeneratedTopology
		if generated.NumCPUs <= 0 || generated.NumSockets <= 0 || generated.NumNUMAs <= 0 {
			return nil, fmt.Errorf("fixture %q has non-positive generated topology parameters",
				fixture.Name)
		}
		topology, err := machine.GenerateDummyCPUTopology(
			generated.NumCPUs, generated.NumSockets, generated.NumNUMAs)
		if err != nil {
			return nil, fmt.Errorf("fixture %q generated topology: %w", fixture.Name, err)
		}
		return topology, nil
	}

	details := make(machine.CPUDetails, len(fixture.CPUs))
	numaIDs, sockets := map[int]struct{}{}, map[int]struct{}{}
	numaSockets := make(map[int]int)
	coreNUMAs := make(map[[2]int]int)
	cores := map[[2]int]struct{}{}
	for _, cpu := range fixture.CPUs {
		if cpu.ID < 0 || cpu.NUMANodeID < 0 || cpu.SocketID < 0 || cpu.CoreID < 0 {
			return nil, fmt.Errorf("fixture %q has negative CPU topology parameter", fixture.Name)
		}
		if _, exists := details[cpu.ID]; exists {
			return nil, fmt.Errorf("fixture %q has duplicate CPU ID %d", fixture.Name, cpu.ID)
		}
		if socketID, exists := numaSockets[cpu.NUMANodeID]; exists && socketID != cpu.SocketID {
			return nil, fmt.Errorf(
				"fixture %q NUMA node %d spans sockets %d and %d",
				fixture.Name, cpu.NUMANodeID, socketID, cpu.SocketID)
		}
		numaSockets[cpu.NUMANodeID] = cpu.SocketID
		coreKey := [2]int{cpu.SocketID, cpu.CoreID}
		if numaID, exists := coreNUMAs[coreKey]; exists && numaID != cpu.NUMANodeID {
			return nil, fmt.Errorf(
				"fixture %q core socket=%d core=%d spans NUMA nodes %d and %d",
				fixture.Name, cpu.SocketID, cpu.CoreID, numaID, cpu.NUMANodeID)
		}
		coreNUMAs[coreKey] = cpu.NUMANodeID
		details[cpu.ID] = machine.CPUTopoInfo{
			NUMANodeID: cpu.NUMANodeID,
			SocketID:   cpu.SocketID,
			CoreID:     cpu.CoreID,
		}
		numaIDs[cpu.NUMANodeID] = struct{}{}
		sockets[cpu.SocketID] = struct{}{}
		cores[coreKey] = struct{}{}
	}
	return &machine.CPUTopology{
		NumCPUs: len(details), NumCores: len(cores),
		NumSockets: len(sockets), NumNUMANodes: len(numaIDs),
		CPUDetails: details,
	}, nil
}

func loadHardReclaimFixture(t *testing.T, name string) (
	hardReclaimFixture,
	*machine.CPUTopology,
	[]partitionDemand,
	machine.CPUSet,
) {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(
		"testdata", "hard_reclaim_replacement", name+".json"))
	require.NoError(t, err)

	fixture, topology, demands, available, err := parseHardReclaimFixture(name, data)
	require.NoError(t, err)
	return fixture, topology, demands, available
}

func setHardReclaimReplacementOptionsForTest(
	t *testing.T,
	topology *machine.CPUTopology,
	mutate func(*hardReclaimReplacementOptions),
) {
	t.Helper()

	options := defaultHardReclaimReplacementOptions()
	mutate(&options)
	hardReclaimReplacementOptionsOverrides.mu.Lock()
	_, exists := hardReclaimReplacementOptionsOverrides.byTopology[topology]
	if exists {
		hardReclaimReplacementOptionsOverrides.mu.Unlock()
		require.FailNow(t, "replacement options already overridden for topology")
	}
	hardReclaimReplacementOptionsOverrides.byTopology[topology] = options
	hardReclaimReplacementOptionsOverrides.mu.Unlock()
	t.Cleanup(func() {
		hardReclaimReplacementOptionsOverrides.mu.Lock()
		delete(hardReclaimReplacementOptionsOverrides.byTopology, topology)
		hardReclaimReplacementOptionsOverrides.mu.Unlock()
	})
}

func TestHardReclaimProductionFixturesCompleteWithinBudgets(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"affected-numa2-boundary",
		"multi-numa-complete-core-deficit",
		"complete-no-feasible",
		"runtime-infeasible-input",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			fixture, topology, demands, available := loadHardReclaimFixture(t, name)
			expectedReclaim, err := machine.Parse(fixture.ExpectedReclaim)
			require.NoError(t, err)

			var first map[string]machine.CPUSet
			var firstDiagnostics hardReclaimSearchDiagnostics
			for run := 0; run < 20; run++ {
				result, solveErr := solveHardReclaimWithReplacementDiagnosed(
					demands, available, topology,
					defaultHardReclaimReplacementOptions())
				if fixture.ExpectCompleteNoFeasible {
					var noFeasible *hardReclaimNoFeasibleReplacement
					require.ErrorAs(t, solveErr, &noFeasible)
					var exhausted *hardReclaimSearchBudgetExceeded
					require.False(t, errors.As(solveErr, &exhausted))
					require.True(t, noFeasible.Diagnostics.Complete)
					require.Equal(t, noFeasible.Diagnostics, result.diagnostics)
					if run == 0 {
						firstDiagnostics = cloneHardReclaimSearchDiagnostics(
							noFeasible.Diagnostics)
						continue
					}
					require.Equal(t, firstDiagnostics, noFeasible.Diagnostics)
					continue
				}

				require.NoError(t, solveErr, "diagnostics=%+v", result.diagnostics)
				require.True(t, result.diagnostics.Complete)
				require.LessOrEqual(t, result.diagnostics.GeneratedCandidateStates,
					hardReclaimReplacementMaxCandidateStates)
				require.LessOrEqual(t, result.diagnostics.TerminalStates,
					hardReclaimReplacementMaxTerminalSolves)
				require.LessOrEqual(t, result.diagnostics.MaxAssignmentEdgesInGraph,
					partitionAssignmentEdgeBudget)
				require.LessOrEqual(t, result.diagnostics.FlowOperations,
					partitionFlowOperationBudget)
				for key, size := range fixture.ExpectedAssignmentSizes {
					require.Equal(t, size, result.assignments[key].Size())
				}
				require.Equal(t, expectedReclaim, result.proof.reclaimAfter)
				requireCoreAligned(t, topology, result.proof.reclaimAfter)
				if run == 0 {
					first = clonePartitionAssignments(result.assignments)
					firstDiagnostics = cloneHardReclaimSearchDiagnostics(result.diagnostics)
					continue
				}
				require.Equal(t, first, result.assignments)
				require.Equal(t, firstDiagnostics, result.diagnostics)
			}
		})
	}
}
