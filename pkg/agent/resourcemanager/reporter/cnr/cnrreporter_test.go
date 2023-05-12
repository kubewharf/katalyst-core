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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
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

func generateTestMetaServer(clientSet *client.GenericClientSet, conf *config.Configuration) *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			NodeFetcher: node.NewRemoteNodeFetcher(conf.NodeName, clientSet.KubeClient.CoreV1().Nodes()),
			CNRFetcher:  cnr.NewCachedCNRFetcher(conf.NodeName, conf.CNRCacheTTL, clientSet.InternalClient.NodeV1alpha1().CustomNodeResources()),
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := initializeFieldToCNR(tt.args.cnr, tt.args.field); (err != nil) != tt.wantErr {
				t.Errorf("initializeFieldToCNR() error = %v, wantErr %v", err, tt.wantErr)
			}

			if !apiequality.Semantic.DeepEqual(tt.args.cnr, tt.wantCNR) {
				t.Errorf("initializeFieldToCNR() got = %v, want %v", tt.args.cnr, tt.wantCNR)
			}
		})
	}
}

func Test_cnrReporterImpl_Update(t *testing.T) {
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
