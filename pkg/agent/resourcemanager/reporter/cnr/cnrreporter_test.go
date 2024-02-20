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

package cnr

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	katalyst_base "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/agent/resourcemanager/reporter"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util"
	"github.com/kubewharf/katalyst-core/pkg/util/syntax"
)

const (
	nodeName = "test-node"
)

func mustParse(str string) *resource.Quantity {
	q := resource.MustParse(str)
	return &q
}

func generateTestMetaServer(clientSet *client.GenericClientSet, conf *config.Configuration) *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			NodeFetcher: node.NewRemoteNodeFetcher(conf.BaseConfiguration, conf.NodeConfiguration,
				clientSet.KubeClient.CoreV1().Nodes()),
			CNRFetcher: cnr.NewCachedCNRFetcher(conf.BaseConfiguration, conf.CNRConfiguration,
				clientSet.InternalClient.NodeV1alpha1().CustomNodeResources()),
		},
	}
}

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	testConfiguration.NodeName = nodeName
	return testConfiguration
}

func generateTestReporter(t *testing.T, defaultCNR *nodev1alpha1.CustomNodeResource) reporter.Reporter {
	testClientSet, err := katalyst_base.GenerateFakeGenericContext(nil, []runtime.Object{defaultCNR})
	require.NoError(t, err)

	testConfiguration := generateTestConfiguration(t)

	testMetaServer := generateTestMetaServer(testClientSet.Client, testConfiguration)
	require.NotNil(t, testMetaServer)

	r, err := NewCNRReporter(testClientSet.Client, testMetaServer, metrics.DummyMetrics{}, generateTestConfiguration(t))
	assert.NoError(t, err)

	return r
}

func testMarshal(t *testing.T, v interface{}) []byte {
	value, err := json.Marshal(v)
	assert.NoError(t, err)
	return value
}

func Test_parseReportFieldToCNR(t *testing.T) {
	t.Parallel()

	type args struct {
		cnr         *nodev1alpha1.CustomNodeResource
		reportField v1alpha1.ReportField
	}
	tests := []struct {
		name    string
		args    args
		want    *nodev1alpha1.CustomNodeResource
		wantErr bool
	}{
		{
			name: "test for cnr property field",
			args: args{
				cnr: &nodev1alpha1.CustomNodeResource{},
				reportField: v1alpha1.ReportField{
					FieldType: v1alpha1.FieldType_Spec,
					FieldName: util.CNRFieldNameNodeResourceProperties,
					Value: []byte(`
							[
								{
									"propertyName": "cpu",
									"propertyQuantity": "10"
								}
							]
					`),
				},
			},
			want: &nodev1alpha1.CustomNodeResource{
				Spec: nodev1alpha1.CustomNodeResourceSpec{
					NodeResourceProperties: []*nodev1alpha1.Property{
						{
							PropertyName:     "cpu",
							PropertyQuantity: resource.NewQuantity(10, resource.DecimalSI),
						},
					},
				},
			},
		},
		{
			name: "test for append array",
			args: args{
				cnr: &nodev1alpha1.CustomNodeResource{
					Spec: nodev1alpha1.CustomNodeResourceSpec{
						NodeResourceProperties: []*nodev1alpha1.Property{
							{
								PropertyName:     "memory",
								PropertyQuantity: resource.NewQuantity(1024*1024*1024*10, resource.BinarySI),
							},
						},
					},
				},
				reportField: v1alpha1.ReportField{
					FieldType: v1alpha1.FieldType_Spec,
					FieldName: util.CNRFieldNameNodeResourceProperties,
					Value: []byte(`
						[
							{
								"propertyName": "cpu",
								"propertyQuantity": "10"
							}
						]
					`),
				},
			},
			want: &nodev1alpha1.CustomNodeResource{
				Spec: nodev1alpha1.CustomNodeResourceSpec{
					NodeResourceProperties: []*nodev1alpha1.Property{
						{
							PropertyName:     "memory",
							PropertyQuantity: resource.NewQuantity(1024*1024*1024*10, resource.BinarySI),
						},
						{
							PropertyName:     "cpu",
							PropertyQuantity: resource.NewQuantity(10, resource.DecimalSI),
						},
					},
				},
			},
		},
		{
			name: "test for set ptr value",
			args: args{
				cnr: &nodev1alpha1.CustomNodeResource{},
				reportField: v1alpha1.ReportField{
					FieldType: v1alpha1.FieldType_Status,
					FieldName: util.CNRFieldNameTopologyZone,
					Value:     []byte(`[{"type": "Socket", "name": "0", "children": [{"type": "Numa", "name": "0", "resources": {"allocatable": {"cpu": "10"}, "capacity": {"cpu": "10"}},"allocations": [{"consumer": "pod0", "requests": {"cpu": "10"}}]}]}]`),
				},
			},
			want: &nodev1alpha1.CustomNodeResource{
				Status: nodev1alpha1.CustomNodeResourceStatus{
					TopologyZone: []*nodev1alpha1.TopologyZone{
						{
							Type: nodev1alpha1.TopologyTypeSocket,
							Name: "0",
							Children: []*nodev1alpha1.TopologyZone{
								{
									Type: nodev1alpha1.TopologyTypeNuma,
									Name: "0",
									Resources: nodev1alpha1.Resources{
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU: resource.MustParse("10"),
										},
										Capacity: &v1.ResourceList{
											v1.ResourceCPU: resource.MustParse("10"),
										},
									},
									Allocations: []*nodev1alpha1.Allocation{
										{
											Consumer: "pod0",
											Requests: &v1.ResourceList{
												v1.ResourceCPU: resource.MustParse("10"),
											},
										},
									},
								},
							},
						},
					},
				},
			},
		},
		{
			name: "test for set label",
			args: args{
				cnr: &nodev1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"cc": "dd",
						},
					},
				},
				reportField: v1alpha1.ReportField{
					FieldType: v1alpha1.FieldType_Metadata,
					FieldName: "Labels",
					Value: []byte(`
						{
							"aa": "bb"
						}
					`),
				},
			},
			want: &nodev1alpha1.CustomNodeResource{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"cc": "dd",
						"aa": "bb",
					},
				},
			},
		},
		{
			name: "set node metric",
			args: args{
				cnr: &nodev1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{
							"cc": "dd",
						},
					},
				},
				reportField: v1alpha1.ReportField{
					FieldType: v1alpha1.FieldType_Status,
					FieldName: "NodeMetricStatus",
					Value: []byte(`
					{
					    "updateTime":"2024-02-08T08:14:20Z",
					    "nodeMetric":{
					        "numaUsage":[
					            {
					                "numaId":0,
					                "usage":{
					                    "memory":"1Gi"
					                }
					            },
					            {
					                "numaId":1,
					                "usage":{
					                    "memory":"1Gi"
					                }
					            }
					        ],
					        "genericUsage":{
					            "cpu":"10",
					            "memory":"2Gi"
					        }
					    },
					    "groupMetric":[
					        {
					            "QoSLevel":"reclaimed_cores",
					            "genericUsage":{
					                "cpu":"0",
					                "memory":"0"
					            }
					        },
					        {
					            "QoSLevel":"shared_cores",
					            "genericUsage":{
					                "cpu":"10",
					                "memory":"1Gi"
					            },
					            "podList":[
					                "platform/pod1",
									"platform/pod2"
					            ]
					        }
					    ]
					}
					`),
				},
			},
			want: &nodev1alpha1.CustomNodeResource{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						"cc": "dd",
					},
				},
				Status: nodev1alpha1.CustomNodeResourceStatus{
					NodeMetricStatus: &nodev1alpha1.NodeMetricStatus{
						UpdateTime: metav1.NewTime(time.Date(2024, time.February, 8, 8, 14, 20, 0, time.UTC)),
						NodeMetric: &nodev1alpha1.NodeMetricInfo{
							ResourceUsage: nodev1alpha1.ResourceUsage{
								NUMAUsage: []nodev1alpha1.NUMAMetricInfo{
									{
										NUMAId: 0,
										Usage: &nodev1alpha1.ResourceMetric{
											Memory: mustParse("1Gi"),
										},
									},
									{
										NUMAId: 1,
										Usage: &nodev1alpha1.ResourceMetric{
											Memory: mustParse("1Gi"),
										},
									},
								},
								GenericUsage: &nodev1alpha1.ResourceMetric{
									CPU:    mustParse("10"),
									Memory: mustParse("2Gi"),
								},
							},
						},
						GroupMetric: []nodev1alpha1.GroupMetricInfo{
							{
								QoSLevel: apiconsts.PodAnnotationQoSLevelReclaimedCores,
								ResourceUsage: nodev1alpha1.ResourceUsage{
									GenericUsage: &nodev1alpha1.ResourceMetric{
										CPU:    mustParse("0"),
										Memory: mustParse("0"),
									},
								},
							},
							{
								QoSLevel: apiconsts.PodAnnotationQoSLevelSharedCores,
								ResourceUsage: nodev1alpha1.ResourceUsage{
									GenericUsage: &nodev1alpha1.ResourceMetric{
										CPU:    mustParse("10"),
										Memory: mustParse("1Gi"),
									},
								},
								PodList: []string{"platform/pod1", "platform/pod2"},
							},
						},
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseReportFieldToCNR(tt.args.cnr, tt.args.reportField, syntax.SimpleMergeTwoValues)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseReportFieldToCNR() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !apiequality.Semantic.DeepEqual(got, tt.want) {
				t.Errorf("parseReportFieldToCNR() \ngot = %#v, \nwant %#v", got, tt.want)
			}
		})
	}
}

func Test_initializeCNRFields(t *testing.T) {
	t.Parallel()

	type args struct {
		cnr   *nodev1alpha1.CustomNodeResource
		field v1alpha1.ReportField
	}
	tests := []struct {
		name    string
		args    args
		wantCNR *nodev1alpha1.CustomNodeResource
		wantErr bool
	}{
		{
			name: "test-for-nil",
			args: args{
				cnr: &nodev1alpha1.CustomNodeResource{
					Spec: nodev1alpha1.CustomNodeResourceSpec{
						NodeResourceProperties: []*nodev1alpha1.Property{
							{
								PropertyName:     "cpu",
								PropertyQuantity: resource.NewQuantity(int64(10), resource.BinarySI),
							},
						},
					},
				},
				field: v1alpha1.ReportField{
					FieldType: v1alpha1.FieldType_Status,
					FieldName: util.CNRFieldNameTopologyZone,
				},
			},
			wantCNR: &nodev1alpha1.CustomNodeResource{
				Spec: nodev1alpha1.CustomNodeResourceSpec{
					NodeResourceProperties: []*nodev1alpha1.Property{
						{
							PropertyName:     "cpu",
							PropertyQuantity: resource.NewQuantity(int64(10), resource.BinarySI),
						},
					},
				},
			},
			wantErr: false,
		},
		{
			name: "test-for-slice-nil",
			args: args{
				cnr: &nodev1alpha1.CustomNodeResource{
					Spec: nodev1alpha1.CustomNodeResourceSpec{
						NodeResourceProperties: []*nodev1alpha1.Property{
							{
								PropertyName:     "cpu",
								PropertyQuantity: resource.NewQuantity(int64(10), resource.BinarySI),
							},
						},
					},
				},
				field: v1alpha1.ReportField{
					FieldType: v1alpha1.FieldType_Spec,
					FieldName: util.CNRFieldNameNodeResourceProperties,
				},
			},
			wantCNR: &nodev1alpha1.CustomNodeResource{
				Spec: nodev1alpha1.CustomNodeResourceSpec{
					NodeResourceProperties: nil,
				},
			},
			wantErr: false,
		},
		{
			name: "test-for-map",
			args: args{
				cnr: &nodev1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"cc": "dd",
						},
					},
				},
				field: v1alpha1.ReportField{
					FieldType: v1alpha1.FieldType_Metadata,
					FieldName: "Annotations",
				},
			},
			wantCNR: &nodev1alpha1.CustomNodeResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: nil,
				},
			},
			wantErr: false,
		},
		{
			name: "test-for-node-status",
			args: args{
				cnr: &nodev1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							"cc": "dd",
						},
					},
					Status: nodev1alpha1.CustomNodeResourceStatus{
						NodeMetricStatus: &nodev1alpha1.NodeMetricStatus{
							UpdateTime:  metav1.Time{},
							NodeMetric:  nil,
							GroupMetric: nil,
						},
					},
				},
				field: v1alpha1.ReportField{
					FieldType: v1alpha1.FieldType_Status,
					FieldName: "NodeMetricStatus",
				},
			},
			wantCNR: &nodev1alpha1.CustomNodeResource{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"cc": "dd",
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := initializeFieldToCNR(tt.args.cnr, tt.args.field); (err != nil) != tt.wantErr {
				t.Errorf("initializeFieldToCNR() error = %v, wantErr %v", err, tt.wantErr)
			}

			assert.Equal(t, tt.wantCNR, tt.args.cnr)
		})
	}
}

func Test_cnrReporterImpl_Update(t *testing.T) {
	t.Parallel()

	type fields struct {
		defaultCNR *nodev1alpha1.CustomNodeResource
	}
	type args struct {
		ctx    context.Context
		fields []*v1alpha1.ReportField
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "test for create with Labels and ResourceAllocatable",
			fields: fields{
				defaultCNR: nil,
			},
			args: args{
				ctx: context.Background(),
				fields: []*v1alpha1.ReportField{
					{
						FieldType: v1alpha1.FieldType_Metadata,
						FieldName: "Labels",
						Value: testMarshal(t, map[string]string{
							"aa": "bb",
						}),
					},
					{
						FieldType: v1alpha1.FieldType_Status,
						FieldName: util.CNRFieldNameResources,
						Value: testMarshal(t, nodev1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("10"),
							},
						}),
					},
				},
			},
		},
		{
			name: "test for update Labels and ResourceAllocatable",
			fields: fields{
				defaultCNR: &nodev1alpha1.CustomNodeResource{
					ObjectMeta: metav1.ObjectMeta{
						Name: nodeName,
					},
				},
			},
			args: args{
				ctx: context.Background(),
				fields: []*v1alpha1.ReportField{
					{
						FieldType: v1alpha1.FieldType_Metadata,
						FieldName: "Labels",
						Value: testMarshal(t, map[string]string{
							"aa": "bb",
						}),
					},
					{
						FieldType: v1alpha1.FieldType_Status,
						FieldName: util.CNRFieldNameResources,
						Value: testMarshal(t, nodev1alpha1.Resources{
							Allocatable: &v1.ResourceList{
								v1.ResourceCPU: resource.MustParse("10"),
							},
						}),
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := generateTestReporter(t, tt.fields.defaultCNR)
			err := r.(cnr.CNRFetcher).RegisterNotifier("test-notifier", cnr.CNRNotifierStub{})
			require.NoError(t, err)
			defer func() {
				err := r.(cnr.CNRFetcher).UnregisterNotifier("test-notifier")
				require.NoError(t, err)
			}()
			go r.Run(context.Background())
			if err := r.Update(tt.args.ctx, tt.args.fields); (err != nil) != tt.wantErr {
				t.Errorf("Update() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
