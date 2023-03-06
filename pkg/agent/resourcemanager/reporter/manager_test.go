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

package reporter

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	internalfake "github.com/kubewharf/katalyst-api/pkg/client/clientset/versioned/fake"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/options"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/cnr"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/agent/node"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

var (
	testGroupVersionKindFirst = v1.GroupVersionKind{
		Group:   "test-group",
		Kind:    "test-kind",
		Version: "test-version",
	}
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
			CNRFetcher:  cnr.NewCachedCNRFetcher(conf.NodeName, conf.CNRCacheTTL, clientSet.InternalClient.NodeV1alpha1().CustomNodeResources()),
		},
	}
}

func generateTestConfiguration(t *testing.T) *config.Configuration {
	testConfiguration, err := options.NewOptions().Config()
	require.NoError(t, err)
	require.NotNil(t, testConfiguration)
	return testConfiguration
}

func TestNewReporterManager(t *testing.T) {
	testClientSet := generateTestGenericClientSet()
	testMetricEmitter := &metrics.DummyMetrics{}
	testConfiguration := generateTestConfiguration(t)

	testMetaServer := generateTestMetaServer(testClientSet, testConfiguration)
	require.NotNil(t, testMetaServer)

	testManager, err := NewReporterManager(generateTestGenericClientSet(), testMetaServer, testMetricEmitter, testConfiguration)
	require.NoError(t, err)
	require.NotNil(t, testManager)
}

func Test_aggregateReportFieldsByGVK(t *testing.T) {
	type args struct {
		reportResponses map[string]*v1alpha1.GetReportContentResponse
	}
	tests := []struct {
		name string
		args args
		want map[v1.GroupVersionKind][]*v1alpha1.ReportField
	}{
		{
			name: "test-1",
			args: args{
				reportResponses: map[string]*v1alpha1.GetReportContentResponse{
					"agent-1": {
						Content: []*v1alpha1.ReportContent{
							{
								GroupVersionKind: &testGroupVersionKindFirst,
								Field: []*v1alpha1.ReportField{
									{
										FieldType: v1alpha1.FieldType_Spec,
										FieldName: "fieldName_a",
										Value:     []byte("Value_a"),
									},
								},
							},
						},
					},
				},
			},
			want: map[v1.GroupVersionKind][]*v1alpha1.ReportField{
				testGroupVersionKindFirst: {
					{
						FieldType: v1alpha1.FieldType_Spec,
						FieldName: "fieldName_a",
						Value:     []byte("Value_a"),
					},
				},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := aggregateReportFieldsByGVK(tt.args.reportResponses); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("aggregateReportFieldsByGVK() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_managerImpl_PushContents(t *testing.T) {
	type fields struct {
		conf      *config.Configuration
		reporters map[v1.GroupVersionKind]Reporter
	}
	type args struct {
		ctx       context.Context
		responses map[string]*v1alpha1.GetReportContentResponse
	}
	tests := []struct {
		name    string
		fields  fields
		args    args
		wantErr bool
	}{
		{
			name: "test-1",
			fields: fields{
				conf: generateTestConfiguration(t),
				reporters: map[v1.GroupVersionKind]Reporter{
					testGroupVersionKindFirst: NewReporterStub(),
				},
			},
			args: args{
				ctx: context.TODO(),
				responses: map[string]*v1alpha1.GetReportContentResponse{
					"agent-1": {
						Content: []*v1alpha1.ReportContent{
							{
								GroupVersionKind: &testGroupVersionKindFirst,
								Field: []*v1alpha1.ReportField{
									{
										FieldType: v1alpha1.FieldType_Spec,
										FieldName: "fieldName_a",
										Value:     []byte("Value_a"),
									},
								},
							},
						},
					},
				},
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &managerImpl{
				conf:      tt.fields.conf,
				reporters: tt.fields.reporters,
			}
			if err := r.PushContents(tt.args.ctx, tt.args.responses); (err != nil) != tt.wantErr {
				t.Errorf("PushContents() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func Test_managerImpl_Run(t *testing.T) {
	type fields struct {
		conf      *config.Configuration
		reporters map[v1.GroupVersionKind]Reporter
	}
	type args struct {
		ctx context.Context
	}
	tests := []struct {
		name   string
		fields fields
		args   args
	}{
		{
			name:   "test-1",
			fields: fields{},
			args: args{
				ctx: context.TODO(),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &managerImpl{
				conf:      tt.fields.conf,
				reporters: tt.fields.reporters,
			}
			go r.Run(tt.args.ctx)
		})
	}
}
