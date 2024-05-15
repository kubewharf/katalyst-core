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

package plugin

import (
	"context"
	"fmt"
	"path"
	"testing"

	"github.com/stretchr/testify/require"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

var testGroupVersionKind = v1.GroupVersionKind{
	Group:   "test-group",
	Kind:    "test-kind",
	Version: "test-version",
}

func TestNewEndpoint(t *testing.T) {
	t.Parallel()

	socketDir := path.Join("/tmp/TestNewEndpoint")

	content := []*v1alpha1.ReportContent{
		{
			GroupVersionKind: &testGroupVersionKind,
			Field:            []*v1alpha1.ReportField{},
		},
	}

	_, gp, e := esetup(t, content, socketDir, "mock", func(n string, d *v1alpha1.GetReportContentResponse) {})
	defer ecleanup(t, gp, e)
}

func TestRun(t *testing.T) {
	t.Parallel()
	socket := path.Join("/tmp/TestRun")

	content := []*v1alpha1.ReportContent{
		{
			GroupVersionKind: &testGroupVersionKind,
			Field: []*v1alpha1.ReportField{
				{
					FieldType: v1alpha1.FieldType_Spec,
					FieldName: "fieldName_a",
					Value:     []byte("Value_a"),
				},
			},
		},
		{
			GroupVersionKind: &testGroupVersionKind,
			Field: []*v1alpha1.ReportField{
				{
					FieldType: v1alpha1.FieldType_Status,
					FieldName: "fieldName_b",
					Value:     []byte("Value_b"),
				},
			},
		},
	}

	updated := []*v1alpha1.ReportContent{
		{
			GroupVersionKind: &testGroupVersionKind,
			Field: []*v1alpha1.ReportField{
				{
					FieldType: v1alpha1.FieldType_Spec,
					FieldName: "fieldName_a",
					Value:     []byte("Value_a_1"),
				},
			},
		},
		{
			GroupVersionKind: &testGroupVersionKind,
			Field: []*v1alpha1.ReportField{
				{
					FieldType: v1alpha1.FieldType_Status,
					FieldName: "fieldName_b",
					Value:     []byte("Value_b_1"),
				},
			},
		},
	}

	callbackCount := 0
	callbackChan := make(chan int)
	callback := func(n string, response *v1alpha1.GetReportContentResponse) {
		// Should be called twice:
		// one for plugin registration, one for plugin update.
		if callbackCount > 2 {
			t.FailNow()
		}

		// Check plugin registration
		if callbackCount == 0 {
			require.Len(t, response.Content, 2)
			reporterContentsEqual(t, response.Content, content)
		}

		// Check plugin update
		if callbackCount == 1 {
			require.Len(t, response.Content, 2)
			reporterContentsEqual(t, response.Content, updated)
		}

		callbackCount++
		callbackChan <- callbackCount
	}

	p, gp, e := esetup(t, content, socket, "mock", callback)
	defer ecleanup(t, gp, e)

	success := make(chan bool)
	go e.Run(success)
	<-success
	// Wait for the first callback to be issued.
	<-callbackChan

	p.Update(updated)

	// Wait for the second callback to be issued.
	<-callbackChan

	require.Equal(t, callbackCount, 2)
}

func TestGetReportContent(t *testing.T) {
	t.Parallel()

	socket := path.Join("/tmp/TestGetReportContent")

	content := []*v1alpha1.ReportContent{
		{
			GroupVersionKind: &testGroupVersionKind,
			Field: []*v1alpha1.ReportField{
				{
					FieldType: v1alpha1.FieldType_Spec,
					FieldName: "fieldName_a",
					Value:     []byte("Value_a"),
				},
			},
		},
		{
			GroupVersionKind: &testGroupVersionKind,
			Field: []*v1alpha1.ReportField{
				{
					FieldType: v1alpha1.FieldType_Status,
					FieldName: "fieldName_b",
					Value:     []byte("Value_b"),
				},
			},
		},
	}

	callbackCount := 0
	callbackChan := make(chan int)
	_, gp, e := esetup(t, content, socket, "mock", func(n string, response *v1alpha1.GetReportContentResponse) {
		callbackCount++
		callbackChan <- callbackCount
	})
	defer ecleanup(t, gp, e)

	respOut, err := e.GetReportContent(context.TODO())
	require.NoError(t, err)
	require.NotNil(t, respOut)
	reporterContentsEqual(t, respOut.Content, content)
}

func esetup(t *testing.T, content []*v1alpha1.ReportContent, socket, pluginName string, callback ListAndWatchCallback) (*skeleton.ReporterPluginStub, skeleton.GenericPlugin, Endpoint) {
	ps := skeleton.NewReporterPluginStub(content, pluginName)
	p, _ := skeleton.NewRegistrationPluginWrapper(ps, []string{socket}, nil)
	err := p.Start()
	require.NoError(t, err)

	e, err := NewRemoteEndpoint(path.Join(socket, fmt.Sprintf("%s.sock", p.Name())), pluginName, nil, metrics.DummyMetrics{}, callback)
	require.NoError(t, err)

	return ps, p, e
}

func ecleanup(t *testing.T, p skeleton.GenericPlugin, e Endpoint) {
	err := p.Stop()
	require.NoError(t, err)
	e.Stop()
}

func reporterContentsEqual(t *testing.T, expected, actual []*v1alpha1.ReportContent) {
	require.Equal(t, len(expected), len(actual))
	for idx := range expected {
		require.Equal(t, expected[idx].GroupVersionKind, actual[idx].GroupVersionKind)
		require.Equal(t, len(expected[idx].Field), len(actual[idx].Field))
		for fieldIdx := range expected[idx].Field {
			require.Equal(t, expected[idx].Field[fieldIdx].FieldType, actual[idx].Field[fieldIdx].FieldType)
			require.Equal(t, expected[idx].Field[fieldIdx].FieldName, actual[idx].Field[fieldIdx].FieldName)
			require.Equal(t, expected[idx].Field[fieldIdx].Value, actual[idx].Field[fieldIdx].Value)
		}
	}
}
