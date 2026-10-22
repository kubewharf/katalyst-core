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

package advisor

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_slowRecover_Moderate(t *testing.T) {
	t.Parallel()
	type fields struct {
		hitCount int
	}
	type args struct {
		raises int
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantCap int
		wantOK  bool
	}{
		{
			name: "nok still warm",
			fields: fields{
				hitCount: 6,
			},
			args: args{
				raises: 1000,
			},
			wantCap: 0,
			wantOK:  false,
		},
		{
			name: "ok if cool",
			fields: fields{
				hitCount: 30,
			},
			args: args{
				raises: 1000,
			},
			wantCap: 20,
			wantOK:  true,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &pipelineRecover{
				recovers: []capRecoveryModerator{
					&lagRecover{
						cooler: coolDown{
							count:             tt.fields.hitCount,
							coolDownThreshold: 30,
						},
					},
					&lagAdjustedBaselineRecover{
						floatingPCT: 2,
						baseline:    10_000,
						cooler: coolDown{
							count:             tt.fields.hitCount,
							coolDownThreshold: 30,
						},
					},
					&reducedRecover{reducerPCT: 10},
				},
			}
			got, got1 := p.Moderate(tt.args.raises)
			assert.Equalf(t, tt.wantCap, got, "Moderate(%v)", tt.args.raises)
			assert.Equalf(t, tt.wantOK, got1, "Moderate(%v)", tt.args.raises)
		})
	}
}

func Test_slowRecover_Accept(t *testing.T) {
	t.Parallel()
	type fields struct {
		hitCount      int
		baselineCount int
	}
	type args struct {
		newCap   int
		currCap  int
		target   int
		observed int
	}
	tests := []struct {
		name                  string
		fields                fields
		args                  args
		wantCoolCount         int
		wantFloor             int
		wantBaselineCoolCount int
	}{
		{
			name: "happy path to cool down and raise baseline (observed below target),",
			fields: fields{
				hitCount:      66,
				baselineCount: 30,
			},
			args: args{
				newCap:   10_111,
				currCap:  10_000,
				target:   100,
				observed: 99,
			},
			wantCoolCount:         67,
			wantBaselineCoolCount: 0, // reset on being cooled down
			wantFloor:             9_888 + 9_888*2/100,
		},
		{
			name: "happy path to cool down one more and no baseline change (observed below target),",
			fields: fields{
				hitCount:      66,
				baselineCount: 20,
			},
			args: args{
				newCap:   10_111,
				currCap:  10_000,
				target:   100,
				observed: 99,
			},
			wantCoolCount:         67,
			wantBaselineCoolCount: 21,
			wantFloor:             9_888,
		},
		{
			name: "being warm resets cool-down count",
			fields: fields{
				hitCount:      55,
				baselineCount: 20,
			},
			args: args{
				newCap:   9_887,
				currCap:  10_002,
				target:   100,
				observed: 100,
			},
			wantCoolCount:         0,     // reset on warm up
			wantBaselineCoolCount: 0,     // reset on warm up
			wantFloor:             9_887, // baseline always respects the less new cap
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			coolDownModerator := &lagRecover{
				cooler: coolDown{
					count:             tt.fields.hitCount,
					coolDownThreshold: 30,
				},
			}
			floorCeilModerator := &lagAdjustedBaselineRecover{
				floatingPCT: 2,
				baseline:    9_888,
				cooler: coolDown{
					count:             tt.fields.baselineCount,
					coolDownThreshold: 30,
				},
			}
			p := &pipelineRecover{
				recovers: []capRecoveryModerator{
					coolDownModerator,
					floorCeilModerator,
					&reducedRecover{reducerPCT: 2},
				},
			}
			p.Accept(tt.args.newCap, tt.args.currCap, tt.args.target, tt.args.observed)
			assert.Equalf(t, tt.wantCoolCount, coolDownModerator.cooler.count, "Accept(%v, %v, %v, %v)", tt.args.newCap, tt.args.currCap, tt.args.target, tt.args.observed)
			assert.Equalf(t, tt.wantBaselineCoolCount, floorCeilModerator.cooler.count, "Accept(%v, %v, %v, %v)", tt.args.newCap, tt.args.currCap, tt.args.target, tt.args.observed)
			assert.Equalf(t, tt.wantFloor, floorCeilModerator.baseline, "Accept(%v, %v, %v, %v)", tt.args.newCap, tt.args.currCap, tt.args.target, tt.args.observed)
		})
	}
}
