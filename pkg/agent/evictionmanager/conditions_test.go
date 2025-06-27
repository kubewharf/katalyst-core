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

package evictionmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	pluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/evictionplugin/v1alpha1"
	reporterpluginapi "github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/utils"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
)

// mockCNRFetcher is a mock implementation of CNRFetcher for testing purposes.
type mockCNRFetcher struct {
	GetCNRFunc func(ctx context.Context) (*v1alpha1.CustomNodeResource, error)
}

func (m *mockCNRFetcher) RegisterNotifier(_ string, _ cnr.CNRNotifier) error {
	return nil
}

func (m *mockCNRFetcher) UnregisterNotifier(_ string) error {
	return nil
}

// GetCNR implements CNRFetcher.GetCNR.
func (m *mockCNRFetcher) GetCNR(ctx context.Context) (*v1alpha1.CustomNodeResource, error) {
	return m.GetCNRFunc(ctx)
}

// mockReporter is a mock implementation of Reporter for testing purposes.
type mockReporter struct {
	ReportContentsFunc func(ctx context.Context, contents []*reporterpluginapi.ReportContent, fastPush bool) error
}

// ReportContents implements Reporter.ReportContents.
func (m *mockReporter) ReportContents(ctx context.Context, contents []*reporterpluginapi.ReportContent, qosAware bool) error {
	return m.ReportContentsFunc(ctx, contents, qosAware)
}

func (m *mockReporter) Run(_ context.Context) error {
	return nil
}

func TestEvictionManger_reportConditionsAsCNRTaints(t *testing.T) {
	t.Parallel()
	as := assert.New(t)

	tests := []struct {
		name string
		// input
		getCNRFunc func(ctx context.Context) (*v1alpha1.CustomNodeResource, error)
		conditions map[string]*pluginapi.Condition
		// mock
		reportContentsFunc func(ctx context.Context, contents []*reporterpluginapi.ReportContent, qosAware bool) error
	}{
		{
			name: "get cnr failed",
			getCNRFunc: func(ctx context.Context) (*v1alpha1.CustomNodeResource, error) {
				return nil, fmt.Errorf("get cnr failed")
			},
			conditions: map[string]*pluginapi.Condition{},
			reportContentsFunc: func(ctx context.Context, contents []*reporterpluginapi.ReportContent, qosAware bool) error {
				return nil
			},
		},
		{
			name: "no conditions",
			getCNRFunc: func(ctx context.Context) (*v1alpha1.CustomNodeResource, error) {
				return &v1alpha1.CustomNodeResource{}, nil
			},
			conditions: map[string]*pluginapi.Condition{},
			reportContentsFunc: func(ctx context.Context, contents []*reporterpluginapi.ReportContent, qosAware bool) error {
				as.Len(contents, 1)
				as.Len(contents[0].Field, 1)
				as.Equal(reporterpluginapi.FieldType_Spec, contents[0].Field[0].FieldType)
				as.Equal(util.CNRFieldNameTaints, contents[0].Field[0].FieldName)
				var taints []v1alpha1.Taint
				err := json.Unmarshal(contents[0].Field[0].Value, &taints)
				as.NoError(err)
				as.Empty(taints)
				return nil
			},
		},
		{
			name: "with cnr conditions",
			getCNRFunc: func(ctx context.Context) (*v1alpha1.CustomNodeResource, error) {
				return &v1alpha1.CustomNodeResource{
					Spec: v1alpha1.CustomNodeResourceSpec{
						Taints: []v1alpha1.Taint{
							{
								Taint: v1.Taint{
									Key:    "test-taint-not-managed",
									Effect: v1.TaintEffectNoSchedule,
								},
							},
						},
					},
				}, nil
			},
			conditions: map[string]*pluginapi.Condition{
				"test-condition-1": {
					ConditionName: "test-condition-1",
					ConditionType: pluginapi.ConditionType_CNR_CONDITION,
					Effects: []string{
						utils.GenerateConditionEffect(apiconsts.QoSLevelReclaimedCores, v1.TaintEffectNoSchedule),
					},
				},
				"test-condition-2": {
					ConditionType: pluginapi.ConditionType_NODE_CONDITION, // Should be ignored
					Effects:       []string{string(v1.TaintEffectNoSchedule)},
				},
			},
			reportContentsFunc: func(ctx context.Context, contents []*reporterpluginapi.ReportContent, qosAware bool) error {
				as.Len(contents, 1)
				as.Len(contents[0].Field, 1)
				as.Equal(reporterpluginapi.FieldType_Spec, contents[0].Field[0].FieldType)
				as.Equal(util.CNRFieldNameTaints, contents[0].Field[0].FieldName)
				var taints []v1alpha1.Taint
				err := json.Unmarshal(contents[0].Field[0].Value, &taints)
				as.NoError(err)
				as.Len(taints, 1)
				for i := range taints {
					as.NotNil(taints[i].TimeAdded)
					// clear TimeAdded for comparison
					taints[i].TimeAdded = nil
				}
				as.Contains(taints, v1alpha1.Taint{
					QoSLevel: apiconsts.QoSLevelReclaimedCores,
					Taint: v1.Taint{
						Key:    getTaintKeyFromConditionName("test-condition-1"),
						Effect: v1.TaintEffectNoSchedule,
					},
				})
				return nil
			},
		},
		{
			name: "report cnr taints failed",
			getCNRFunc: func(ctx context.Context) (*v1alpha1.CustomNodeResource, error) {
				return &v1alpha1.CustomNodeResource{}, nil
			},
			conditions: map[string]*pluginapi.Condition{
				"test-condition-1": {
					ConditionType: pluginapi.ConditionType_CNR_CONDITION,
					Effects: []string{
						utils.GenerateConditionEffect(apiconsts.QoSLevelReclaimedCores, v1.TaintEffectNoSchedule),
					},
				},
			},
			reportContentsFunc: func(ctx context.Context, contents []*reporterpluginapi.ReportContent, qosAware bool) error {
				return fmt.Errorf("report cnr taints failed")
			},
		},
		{
			name: "complex cnr taints interaction",
			getCNRFunc: func(ctx context.Context) (*v1alpha1.CustomNodeResource, error) {
				return &v1alpha1.CustomNodeResource{
					Spec: v1alpha1.CustomNodeResourceSpec{
						Taints: []v1alpha1.Taint{
							{
								Taint: v1.Taint{
									Key:    "test-taint-unmanaged-1", // Unmanaged, should be kept
									Effect: v1.TaintEffectNoExecute,
								},
							},
							{
								QoSLevel: apiconsts.QoSLevelReclaimedCores,
								Taint: v1.Taint{
									Key:    getTaintKeyFromConditionName("old-managed-condition"), // Managed, but not in new conditions, should be removed
									Effect: v1.TaintEffectNoSchedule,
								},
							},
						},
					},
				}, nil
			},
			conditions: map[string]*pluginapi.Condition{
				"new-condition-1": {
					ConditionName: "new-condition-1",
					ConditionType: pluginapi.ConditionType_CNR_CONDITION,
					Effects: []string{
						utils.GenerateConditionEffect(apiconsts.QoSLevelReclaimedCores, v1.TaintEffectNoSchedule),
					},
				},
			},
			reportContentsFunc: func(ctx context.Context, contents []*reporterpluginapi.ReportContent, qosAware bool) error {
				as.Len(contents, 1)
				as.Len(contents[0].Field, 1)
				var taints []v1alpha1.Taint
				err := json.Unmarshal(contents[0].Field[0].Value, &taints)
				as.NoError(err)
				as.Len(taints, 1) // new-condition-1
				for i := range taints {
					as.NotNil(taints[i].TimeAdded)
					// clear TimeAdded for comparison
					taints[i].TimeAdded = nil
				}
				as.Contains(taints, v1alpha1.Taint{
					QoSLevel: apiconsts.QoSLevelReclaimedCores,
					Taint: v1.Taint{
						Key:    getTaintKeyFromConditionName("new-condition-1"),
						Effect: v1.TaintEffectNoSchedule,
					},
				})
				return nil
			},
		},
		{
			name: "invalid condition effects",
			getCNRFunc: func(ctx context.Context) (*v1alpha1.CustomNodeResource, error) {
				return &v1alpha1.CustomNodeResource{}, nil
			},
			conditions: map[string]*pluginapi.Condition{
				"valid-condition": {
					ConditionName: "valid-condition",
					ConditionType: pluginapi.ConditionType_CNR_CONDITION,
					Effects: []string{
						utils.GenerateConditionEffect(apiconsts.QoSLevelReclaimedCores, v1.TaintEffectNoSchedule),
					},
				},
				"invalid-effect-format": {
					ConditionName: "invalid-effect-format",
					ConditionType: pluginapi.ConditionType_CNR_CONDITION,
					Effects:       []string{"InvalidFormat"}, // Missing separator
				},
				"invalid-qos-level": {
					ConditionName: "invalid-qos-level",
					ConditionType: pluginapi.ConditionType_CNR_CONDITION,
					Effects: []string{
						utils.GenerateConditionEffect("InvalidQoS", v1.TaintEffectNoSchedule),
					},
				},
				"invalid-taint-effect": {
					ConditionName: "invalid-taint-effect",
					ConditionType: pluginapi.ConditionType_CNR_CONDITION,
					Effects: []string{
						utils.GenerateConditionEffect(apiconsts.QoSLevelReclaimedCores, "InvalidEffectName"),
					},
				},
				"repeated-effect": {
					ConditionName: "repeated-effect",
					ConditionType: pluginapi.ConditionType_CNR_CONDITION,
					Effects: []string{
						utils.GenerateConditionEffect(apiconsts.QoSLevelReclaimedCores, v1.TaintEffectPreferNoSchedule),
						utils.GenerateConditionEffect(apiconsts.QoSLevelReclaimedCores, v1.TaintEffectPreferNoSchedule),
					},
				},
			},
			reportContentsFunc: func(ctx context.Context, contents []*reporterpluginapi.ReportContent, qosAware bool) error {
				as.Len(contents, 1)
				as.Len(contents[0].Field, 1)
				var taints []v1alpha1.Taint
				err := json.Unmarshal(contents[0].Field[0].Value, &taints)
				as.NoError(err)
				// Only valid-condition and the first occurrence of repeated-effect's valid part should be present
				as.Len(taints, 2)
				for i := range taints {
					as.NotNil(taints[i].TimeAdded)
					// clear TimeAdded for comparison
					taints[i].TimeAdded = nil
				}
				as.Contains(taints, v1alpha1.Taint{
					QoSLevel: apiconsts.QoSLevelReclaimedCores,
					Taint: v1.Taint{
						Key:    getTaintKeyFromConditionName("valid-condition"),
						Effect: v1.TaintEffectNoSchedule,
					},
				})
				as.Contains(taints, v1alpha1.Taint{
					QoSLevel: apiconsts.QoSLevelReclaimedCores,
					Taint: v1.Taint{
						Key:    getTaintKeyFromConditionName("repeated-effect"),
						Effect: v1.TaintEffectPreferNoSchedule,
					},
				})
				return nil
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			m := &EvictionManger{
				metaGetter: &metaserver.MetaServer{
					MetaAgent: &agent.MetaAgent{
						CNRFetcher: &mockCNRFetcher{
							GetCNRFunc: tt.getCNRFunc,
						},
					},
				},
				cnrTaintReporter: &mockReporter{
					ReportContentsFunc: tt.reportContentsFunc,
				},
				conditions: tt.conditions,
				// Initialize other fields if necessary
				conf:    config.NewConfiguration(),
				emitter: metrics.DummyMetrics{},
			}

			m.reportConditionsAsCNRTaints(context.Background())
		})
	}
}
