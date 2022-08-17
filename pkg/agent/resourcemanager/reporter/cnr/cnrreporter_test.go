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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
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
	apiequality "k8s.io/apimachinery/pkg/api/equality"
)

func generateTestGenericClientSet(objects ...runtime.Object) *client.GenericClientSet {
	return &client.GenericClientSet{
		KubeClient:     fake.NewSimpleClientset(objects...),
		InternalClient: internalfake.NewSimpleClientset(objects...),
		DynamicClient:  dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), objects...),
	}
}

func generateTestMetaServer(clientSet *client.GenericClientSet, conf *config.Configuration) *metaserver.MetaServer {
	return &metaserver.MetaServer{
		MetaAgent: &agent.MetaAgent{
			NodeFetcher: node.NewRemoteNodeFetcher(conf.NodeName, clientSet.KubeClient.CoreV1().Nodes()),
			CNRFetcher:  cnr.NewRemoteCNRFetcher(conf.NodeName, clientSet.InternalClient.NodeV1alpha1().CustomNodeResources()),
		},
	}
}

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

func generateTestReporter(t *testing.T) reporter.Reporter {
	testClientSet := generateTestGenericClientSet()
	testConfiguration := generateTestConfiguration(t)

	testMetaServer := generateTestMetaServer(testClientSet, testConfiguration)
	require.NotNil(t, testMetaServer)

	r, err := NewCNRReporter(generateTestGenericClientSet(), testMetaServer, metrics.DummyMetrics{}, generateTestConfiguration(t))
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
					FieldName: util.CNRFieldNameTopologyStatus,
					Value: []byte(`{
						  "sockets": [
							{
							  "socketID": 0,
							  "numas": [
								{
								  "numaID": 0,
								  "allocatable": {
									"cpu": "10"
								  },
								  "capacity": {
									"cpu": "10"
								  },
								  "allocations": [
									{
									  "consumer": "pod0",
									  "requests": {
										"cpu": "10"
									  } 
									}
								  ]
								}
							  ]
							}
						  ]
						}
					`),
				},
			},
			want: &nodev1alpha1.CustomNodeResource{
				Status: nodev1alpha1.CustomNodeResourceStatus{
					TopologyStatus: &nodev1alpha1.TopologyStatus{
						Sockets: []*nodev1alpha1.SocketStatus{
							{
								SocketID: 0,
								Numas: []*nodev1alpha1.NumaStatus{
									{
										NumaID: 0,
										Allocatable: &v1.ResourceList{
											v1.ResourceCPU: resource.MustParse("10"),
										},
										Capacity: &v1.ResourceList{
											v1.ResourceCPU: resource.MustParse("10"),
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
					FieldName: util.CNRFieldNameTopologyStatus,
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
		r reporter.Reporter
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
			name: "test for Labels and ResourceAllocatable",
			fields: fields{
				r: generateTestReporter(t),
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
						FieldName: util.CNRFieldNameResourceAllocatable,
						Value: testMarshal(t, v1.ResourceList{
							v1.ResourceCPU: resource.MustParse("10"),
						}),
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			go tt.fields.r.Run(context.Background())
			if err := tt.fields.r.Update(tt.args.ctx, tt.args.fields); (err != nil) != tt.wantErr {
				t.Errorf("Update() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
