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

package control

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/mockey"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

var klogFatalf = klog.Fatalf

func TestGenericReporterPlugin_GetReportContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		started   bool
		resp      *v1alpha1.GetReportContentResponse
		expectErr bool
	}{
		{
			name:      "not started",
			started:   false,
			resp:      nil,
			expectErr: true,
		},
		{
			name:      "started but no content",
			started:   true,
			resp:      nil,
			expectErr: true,
		},
		{
			name:    "started with content",
			started: true,
			resp: &v1alpha1.GetReportContentResponse{
				Content: []*v1alpha1.ReportContent{
					{
						GroupVersionKind: &v1.GroupVersionKind{
							Group:   "test",
							Version: "v1",
							Kind:    "Test",
						},
						Field: []*v1alpha1.ReportField{
							{
								FieldType: v1alpha1.FieldType_Spec,
								FieldName: "testField",
								Value:     []byte("testValue"),
							},
						},
					},
				},
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &genericReporterPlugin{
				started: tt.started,
				resp:    tt.resp,
			}

			resp, err := c.GetReportContent(context.TODO(), &v1alpha1.Empty{})

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, resp)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.resp, resp)
			}
		})
	}
}

type doneContext struct {
	context.Context
	err error
}

func (d *doneContext) Done() <-chan struct{} {
	c := make(chan struct{})
	close(c)
	return c
}

func (d *doneContext) Err() error {
	return d.err
}

func TestGenericReporterPlugin_ReportContents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		initialResp  *v1alpha1.GetReportContentResponse
		contents     []*v1alpha1.ReportContent
		fastPush     bool
		expectNotify bool
		expectErr    bool
	}{
		{
			name:        "normal update",
			initialResp: nil,
			contents: []*v1alpha1.ReportContent{
				{
					GroupVersionKind: &v1.GroupVersionKind{
						Group:   "test",
						Version: "v1",
						Kind:    "Test",
					},
					Field: []*v1alpha1.ReportField{
						{
							FieldType: v1alpha1.FieldType_Spec,
							FieldName: "testField",
							Value:     []byte("testValue"),
						},
					},
				},
			},
			fastPush:     false,
			expectNotify: false,
			expectErr:    false,
		},
		{
			name: "fast push with same content and notifier",
			initialResp: &v1alpha1.GetReportContentResponse{
				Content: []*v1alpha1.ReportContent{
					{
						GroupVersionKind: &v1.GroupVersionKind{
							Group:   "test",
							Version: "v1",
							Kind:    "Test",
						},
						Field: []*v1alpha1.ReportField{
							{
								FieldType: v1alpha1.FieldType_Spec,
								FieldName: "testField",
								Value:     []byte("testValue"),
							},
						},
					},
				},
			},
			contents: []*v1alpha1.ReportContent{
				{
					GroupVersionKind: &v1.GroupVersionKind{
						Group:   "test",
						Version: "v1",
						Kind:    "Test",
					},
					Field: []*v1alpha1.ReportField{
						{
							FieldType: v1alpha1.FieldType_Spec,
							FieldName: "testField",
							Value:     []byte("testValue"),
						},
					},
				},
			},
			fastPush:     true,
			expectNotify: false,
			expectErr:    false,
		},
		{
			name: "fast push with different content",
			initialResp: &v1alpha1.GetReportContentResponse{
				Content: []*v1alpha1.ReportContent{},
			},
			contents: []*v1alpha1.ReportContent{
				{
					GroupVersionKind: &v1.GroupVersionKind{
						Group:   "test",
						Version: "v1",
						Kind:    "Test",
					},
					Field: []*v1alpha1.ReportField{
						{
							FieldType: v1alpha1.FieldType_Spec,
							FieldName: "testField",
							Value:     []byte("testValue"),
						},
					},
				},
			},
			fastPush:     true,
			expectNotify: true,
			expectErr:    false,
		},
		{
			name: "fast push with same content but no notifier",
			initialResp: &v1alpha1.GetReportContentResponse{
				Content: []*v1alpha1.ReportContent{
					{
						GroupVersionKind: &v1.GroupVersionKind{
							Group:   "test",
							Version: "v1",
							Kind:    "Test",
						},
						Field: []*v1alpha1.ReportField{
							{
								FieldType: v1alpha1.FieldType_Spec,
								FieldName: "testField",
								Value:     []byte("testValue"),
							},
						},
					},
				},
			},
			contents: []*v1alpha1.ReportContent{
				{
					GroupVersionKind: &v1.GroupVersionKind{
						Group:   "test",
						Version: "v1",
						Kind:    "Test",
					},
					Field: []*v1alpha1.ReportField{
						{
							FieldType: v1alpha1.FieldType_Spec,
							FieldName: "testField",
							Value:     []byte("testValue"),
						},
					},
				},
			},
			fastPush:     true,
			expectNotify: false,
			expectErr:    false,
		},
		{
			name: "fast push with context cancellation",
			initialResp: &v1alpha1.GetReportContentResponse{
				Content: []*v1alpha1.ReportContent{},
			},
			contents: []*v1alpha1.ReportContent{
				{
					GroupVersionKind: &v1.GroupVersionKind{
						Group:   "test",
						Version: "v1",
						Kind:    "Test",
					},
					Field: []*v1alpha1.ReportField{
						{
							FieldType: v1alpha1.FieldType_Spec,
							FieldName: "testField",
							Value:     []byte("testValue"),
						},
					},
				},
			},
			fastPush:     true,
			expectNotify: true,
			expectErr:    true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &genericReporterPlugin{
				resp: tt.initialResp,
			}

			var notifyCh chan struct{}
			if tt.expectNotify {
				notifyCh = make(chan struct{}, 1)
				c.notifierCh = notifyCh
			}

			ctx := context.TODO()
			if tt.name == "fast push with context cancellation" {
				ctx = &doneContext{ctx, errors.New("context cancelled")}
				notifyCh <- struct{}{}
			}

			err := c.ReportContents(ctx, tt.contents, tt.fastPush)
			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Verify resp is updated
			assert.True(t, apiequality.Semantic.DeepEqual(tt.contents, c.resp.Content))

			if tt.expectNotify && !tt.expectErr {
				select {
				case <-notifyCh:
					// Notification received
				case <-time.After(time.Second):
					assert.Fail(t, "did not receive notification")
				}
			}
		})
	}
}

type mockReporterPluginListAndWatchReportContentServer struct {
	lock *sync.RWMutex
	grpc.ServerStream
	sendCount int
	resp      *v1alpha1.GetReportContentResponse
	err       error
	ctx       context.Context
}

func (m *mockReporterPluginListAndWatchReportContentServer) Send(resp *v1alpha1.GetReportContentResponse) error {
	m.lock.Lock()
	defer m.lock.Unlock()
	m.sendCount++
	m.resp = resp
	return m.err
}

func (m *mockReporterPluginListAndWatchReportContentServer) Context() context.Context {
	return m.ctx
}

func TestGenericReporterPlugin_ListAndWatchReportContent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		started         bool
		getResp         *v1alpha1.GetReportContentResponse
		restartFunc     func() error
		sendErr         error
		expectErr       bool
		expectSendCount int
		cancelContext   bool
		stopPlugin      bool
	}{
		{
			name:      "plugin not started",
			started:   false,
			expectErr: true,
		},
		{
			name:            "normal flow",
			started:         true,
			getResp:         &v1alpha1.GetReportContentResponse{},
			expectSendCount: 1,
		},
		{
			name:      "get report content error",
			started:   true,
			expectErr: false, // The loop continues, no error returned from function itself
		},
		{
			name:            "send error but restart error",
			started:         true,
			getResp:         &v1alpha1.GetReportContentResponse{},
			sendErr:         errors.New("send error"),
			restartFunc:     func() error { return nil },
			expectErr:       false,
			expectSendCount: 1,
		},
		{
			name:            "context cancelled",
			started:         true,
			getResp:         &v1alpha1.GetReportContentResponse{},
			expectSendCount: 0, // No send happens if context is cancelled before loop
			cancelContext:   true,
			expectErr:       false,
		},
		{
			name:            "plugin stopped",
			started:         true,
			getResp:         &v1alpha1.GetReportContentResponse{},
			expectSendCount: 0, // No send happens if plugin is stopped before loop
			stopPlugin:      true,
			expectErr:       false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			stopCh := make(chan struct{})
			if !tt.stopPlugin {
				defer close(stopCh)
			}

			c := &genericReporterPlugin{
				started:     tt.started,
				stop:        stopCh,
				resp:        tt.getResp,
				restartFunc: tt.restartFunc,
			}

			if tt.cancelContext {
				cancel()
			}
			if tt.stopPlugin {
				close(stopCh)
			}

			mockServer := &mockReporterPluginListAndWatchReportContentServer{
				lock: &c.RWMutex,
				err:  tt.sendErr,
				ctx:  ctx,
			}

			go func() {
				err := c.ListAndWatchReportContent(&v1alpha1.Empty{}, mockServer)
				if tt.expectErr {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
			}()

			// For normal flow, we expect one send. For error cases, it depends on when the error occurs.
			if tt.name == "normal flow" || tt.name == "get report content error" {
				time.Sleep(2 * time.Second)
				// Trigger a notification to ensure the send path is taken
				c.RLock()
				c.notifierCh <- struct{}{}
				c.RUnlock()
				// Give some time for the goroutine to process the notification
				time.Sleep(2 * time.Second)
				c.RLock()
				assert.Equal(t, tt.expectSendCount, mockServer.sendCount)
				assert.Equal(t, tt.getResp, mockServer.resp)
				c.RUnlock()
			} else if tt.name == "send error but restart error" {
				time.Sleep(2 * time.Second)
				// Trigger a notification to ensure the send path is taken
				c.RLock()
				c.notifierCh <- struct{}{}
				c.RUnlock()
				// Give some time for the goroutine to process the notification
				time.Sleep(2 * time.Second)
				c.RLock()
				assert.Equal(t, tt.expectSendCount, mockServer.sendCount)
				c.RUnlock()
			} else {
				time.Sleep(2 * time.Second)
				c.RLock()
				assert.Equal(t, tt.expectSendCount, mockServer.sendCount)
				c.RUnlock()
			}
		})
	}
}

func TestGenericReporterPlugin_Run(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		startErr    error
		stopErr     error
		expectError bool
	}{
		{
			name:        "normal run",
			startErr:    nil,
			stopErr:     nil,
			expectError: false,
		},
		{
			name:        "start error",
			startErr:    errors.New("start failed"),
			stopErr:     nil,
			expectError: true,
		},
		{
			name:        "stop error",
			startErr:    nil,
			stopErr:     errors.New("stop failed"),
			expectError: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &genericReporterPlugin{
				name:      "test-plugin",
				startFunc: func() error { return tt.startErr },
				stopFunc:  func() error { return tt.stopErr },
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			go func() {
				err := c.Run(ctx)
				if tt.expectError {
					assert.Error(t, err)
				} else {
					assert.NoError(t, err)
				}
				time.Sleep(2 * time.Second)
			}()
		})
	}
}

func TestGenericReporterPlugin_Start(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		initialStarted bool
		expectStarted  bool
		expectErr      bool
	}{
		{
			name:           "not started initially",
			initialStarted: false,
			expectStarted:  true,
			expectErr:      false,
		},
		{
			name:           "already started",
			initialStarted: true,
			expectStarted:  true,
			expectErr:      false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := &genericReporterPlugin{
				started: tt.initialStarted,
			}

			err := c.Start()

			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectStarted, c.started)
			if !tt.initialStarted {
				assert.NotNil(t, c.stop)
			}
		})
	}
}

func TestGenericReporterPlugin_Stop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		initialStarted     bool
		expectStarted      bool
		expectErr          bool
		expectStopChClosed bool
	}{
		{
			name:               "started initially",
			initialStarted:     true,
			expectStarted:      false,
			expectErr:          false,
			expectStopChClosed: true,
		},
		{
			name:               "not started initially",
			initialStarted:     false,
			expectStarted:      false,
			expectErr:          false,
			expectStopChClosed: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stopCh := make(chan struct{})
			c := &genericReporterPlugin{
				started: tt.initialStarted,
				stop:    stopCh,
			}

			err := c.Stop()

			if tt.expectErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectStarted, c.started)

			if tt.expectStopChClosed {
				select {
				case <-stopCh:
					// Channel is closed, as expected
				default:
					assert.Fail(t, "stop channel was not closed")
				}
			} else {
				select {
				case <-stopCh:
					assert.Fail(t, "stop channel was unexpectedly closed")
				default:
					// Channel is not closed, as expected
				}
			}
		})
	}
}

func TestNewGenericReporterPlugin(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		pluginName       string
		registrationDir  string
		newWrapperErr    error
		expectErr        bool
		expectPluginName string
	}{
		{
			name:             "success case",
			pluginName:       "test-plugin",
			registrationDir:  "/tmp/test",
			newWrapperErr:    nil,
			expectErr:        false,
			expectPluginName: "test-plugin",
		},
		{
			name:            "new wrapper error",
			pluginName:      "test-plugin-error",
			registrationDir: "/tmp/test-error",
			newWrapperErr:   errors.New("failed to create wrapper"),
			expectErr:       true,
		},
	}

	for _, tt := range tests {
		mockey.PatchConvey(tt.name, t, func() {
			// Arrange
			mockEmitter := metrics.DummyMetrics{}
			conf := config.NewConfiguration()
			conf.PluginRegistrationDir = tt.registrationDir

			mockey.Mock(skeleton.NewRegistrationPluginWrapper).To(func(plugin skeleton.GenericPlugin, pluginsRegistrationDirs []string,
				metricCallback skeleton.MetricCallback,
			) (*skeleton.PluginRegistrationWrapper, error) {
				assert.Equal(t, []string{tt.registrationDir}, pluginsRegistrationDirs)
				metricCallback("", 1)
				return &skeleton.PluginRegistrationWrapper{},
					tt.newWrapperErr
			}).Build()

			// Act
			reporter, err := NewGenericReporterPlugin(tt.pluginName, conf, mockEmitter)

			// Assert
			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, reporter)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, reporter)

				// Type assertion to access internal fields for verification
				genericReporter, ok := reporter.(*genericReporterPlugin)
				assert.True(t, ok)
				assert.Equal(t, tt.expectPluginName, genericReporter.name)
				assert.NotNil(t, genericReporter.restartFunc)
				assert.NotNil(t, genericReporter.startFunc)
				assert.NotNil(t, genericReporter.stopFunc)
			}
		})
	}
}
