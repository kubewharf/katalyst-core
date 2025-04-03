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

package strategygroup

import (
	"reflect"
	"testing"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/strategygroup"
)

func Test_validateConf(t *testing.T) {
	t.Parallel()
	type args struct {
		conf *config.Configuration
	}
	tests := []struct {
		name    string
		args    args
		wantErr bool
	}{
		{
			name:    "nil conf",
			wantErr: true,
		},
		{
			name: "nil agent conf",
			args: args{
				conf: &config.Configuration{},
			},
			wantErr: true,
		},
		{
			name: "nil dynamic agent conf",
			args: args{
				conf: &config.Configuration{
					AgentConfiguration: &agent.AgentConfiguration{
						DynamicAgentConfiguration: &dynamic.DynamicAgentConfiguration{},
					},
				},
			},
			wantErr: true,
		},
		{
			name: "nil dynamic conf",
			args: args{
				conf: &config.Configuration{
					AgentConfiguration: &agent.AgentConfiguration{},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		copiedTT := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := validateConf(copiedTT.args.conf)
			if (err != nil) != copiedTT.wantErr {
				t.Errorf("validateConf() error = %v, wantErr %v", err, copiedTT.wantErr)
			}
		})
	}
}

func TestIsStrategyEnabledForNode(t *testing.T) {
	t.Parallel()
	gloalConf := &config.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			DynamicAgentConfiguration: &dynamic.DynamicAgentConfiguration{},
		},
	}

	sa, sb := "sa", "sb"

	gloalConf.SetDynamicConfiguration(&dynamic.Configuration{
		StrategyGroup: &strategygroup.StrategyGroup{
			EnabledStrategies: []v1alpha1.Strategy{
				{
					Name: &sa,
				},
				{
					Name: &sb,
				},
			},
		},
	})
	type args struct {
		strategyName string
		defaultValue bool
		conf         *config.Configuration
	}
	tests := []struct {
		name    string
		args    args
		want    bool
		wantErr bool
	}{
		{
			name: "normal case",
			args: args{
				strategyName: sa,
				defaultValue: false,
				conf:         gloalConf,
			},
			want:    true,
			wantErr: false,
		},
	}
	for _, tt := range tests {
		copiedTT := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := IsStrategyEnabledForNode(copiedTT.args.strategyName, copiedTT.args.defaultValue, copiedTT.args.conf)
			if (err != nil) != copiedTT.wantErr {
				t.Errorf("IsStrategyEnabledForNode() error = %v, wantErr %v", err, copiedTT.wantErr)
				return
			}
			if got != copiedTT.want {
				t.Errorf("IsStrategyEnabledForNode() = %v, want %v", got, copiedTT.want)
			}
		})
	}
}

func TestGetEnabledStrategiesForNode(t *testing.T) {
	t.Parallel()
	gloalConf := &config.Configuration{
		AgentConfiguration: &agent.AgentConfiguration{
			DynamicAgentConfiguration: &dynamic.DynamicAgentConfiguration{},
		},
	}

	sa, sb := "sa", "sb"

	gloalConf.SetDynamicConfiguration(&dynamic.Configuration{
		StrategyGroup: &strategygroup.StrategyGroup{
			EnabledStrategies: []v1alpha1.Strategy{
				{
					Name: &sa,
				},
				{
					Name: &sb,
				},
			},
		},
	})
	type args struct {
		conf *config.Configuration
	}
	tests := []struct {
		name    string
		args    args
		want    []string
		wantErr bool
	}{
		{
			name: "normal case",
			args: args{
				conf: gloalConf,
			},
			want:    []string{sa, sb},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		copiedTT := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := GetEnabledStrategiesForNode(copiedTT.args.conf)
			if (err != nil) != copiedTT.wantErr {
				t.Errorf("GetEnabledStrategiesForNode() error = %v, wantErr %v", err, copiedTT.wantErr)
				return
			}
			if !reflect.DeepEqual(got, copiedTT.want) {
				t.Errorf("GetEnabledStrategiesForNode() = %v, want %v", got, copiedTT.want)
			}
		})
	}
}
