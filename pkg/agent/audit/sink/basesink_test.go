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

package sink

import (
	"context"
	"errors"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"github.com/stretchr/testify/mock"

	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/eventbus"
)

// MockEventBus is a mock implementation of eventbus.EventBus
type MockEventBus struct {
	mock.Mock
}

func (m *MockEventBus) Publish(topic string, event interface{}) error {
	args := m.Called(topic, event)
	return args.Error(0)
}

func (m *MockEventBus) Subscribe(topic string, subscriber string, bufferSize int, handler eventbus.ConsumeFunc) error {
	args := m.Called(topic, subscriber, bufferSize, handler)
	return args.Error(0)
}

func (m *MockEventBus) EnableStatistic() {
	m.Called()
}

func (m *MockEventBus) SetEmitter(emitter metrics.MetricEmitter) {
	m.Called(emitter)
}

// MockAuditSink is a mock implementation of Interface
type MockAuditSink struct {
	Interface
	name        string
	bufferSize  int
	handlerFunc eventbus.ConsumeFunc
}

func (m *MockAuditSink) GetHandler() eventbus.ConsumeFunc {
	return m.handlerFunc
}

func (m *MockAuditSink) GetName() string {
	return m.name
}

func (m *MockAuditSink) GetBufferSize() int {
	return m.bufferSize
}

func TestBaseAuditSink_Run(t *testing.T) {
	t.Parallel()
	Convey("Test BaseAuditSink Run method", t, func() {
		// Create a mock sink that implements Interface
		mockSink := &MockAuditSink{
			name:       "test-sink",
			bufferSize: 100,
			handlerFunc: func(interface{}) error {
				return nil
			},
		}

		// Create a base sink with the mock sink
		baseSink := &BaseAuditSink{
			Interface: mockSink,
		}

		// Test case 1: All subscriptions succeed
		Convey("When all subscriptions succeed", func() {
			// Create a mock event bus
			mockBus := &MockEventBus{}

			// Expect Subscribe to be called for each topic
			mockBus.On("Subscribe", consts.TopicNameApplyCGroup, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(nil)
			mockBus.On("Subscribe", consts.TopicNameApplyProcFS, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(nil)
			mockBus.On("Subscribe", consts.TopicNameApplySysFS, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(nil)
			mockBus.On("Subscribe", consts.TopicNameSyscall, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(nil)

			// Create a context that can be canceled
			ctx, cancel := context.WithCancel(context.Background())

			// Run the base sink in a goroutine
			go baseSink.Run(ctx, mockBus)

			// Wait a moment to ensure subscriptions are processed
			time.Sleep(100 * time.Millisecond)

			// Cancel the context to stop the Run method
			cancel()

			// Verify that all Subscribe methods were called
			mockBus.AssertExpectations(t)
		})

		// Test case 2: Some subscriptions fail
		Convey("When some subscriptions fail", func() {
			// Create a mock event bus
			mockBus := &MockEventBus{}

			// Expect some Subscribe calls to fail
			mockBus.On("Subscribe", consts.TopicNameApplyCGroup, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(nil)
			mockBus.On("Subscribe", consts.TopicNameApplyProcFS, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(errors.New("subscribe failed"))
			mockBus.On("Subscribe", consts.TopicNameApplySysFS, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(nil)
			mockBus.On("Subscribe", consts.TopicNameSyscall, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(errors.New("subscribe failed"))

			// Create a context that can be canceled
			ctx, cancel := context.WithCancel(context.Background())

			// Run the base sink in a goroutine
			go baseSink.Run(ctx, mockBus)

			// Wait a moment to ensure subscriptions are processed
			time.Sleep(100 * time.Millisecond)

			// Cancel the context to stop the Run method
			cancel()

			// Verify that all Subscribe methods were called
			mockBus.AssertExpectations(t)
		})

		// Test case 3: Context is canceled immediately
		Convey("When context is canceled immediately", func() {
			// Create a mock event bus
			mockBus := &MockEventBus{}

			// Create a context that is already canceled
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			// Expect Subscribe to be called for each topic but return context canceled error
			mockBus.On("Subscribe", consts.TopicNameApplyCGroup, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(context.Canceled)
			mockBus.On("Subscribe", consts.TopicNameApplyProcFS, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(context.Canceled)
			mockBus.On("Subscribe", consts.TopicNameApplySysFS, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(context.Canceled)
			mockBus.On("Subscribe", consts.TopicNameSyscall, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc")).Return(context.Canceled)

			// Run the base sink
			baseSink.Run(ctx, mockBus)

			// Verify that subscriptions were made but failed due to canceled context
			mockBus.AssertCalled(t, "Subscribe", consts.TopicNameApplyCGroup, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc"))
			mockBus.AssertCalled(t, "Subscribe", consts.TopicNameApplyProcFS, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc"))
			mockBus.AssertCalled(t, "Subscribe", consts.TopicNameApplySysFS, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc"))
			mockBus.AssertCalled(t, "Subscribe", consts.TopicNameSyscall, mockSink.GetName(), mockSink.GetBufferSize(), mock.AnythingOfType("eventbus.ConsumeFunc"))
		})
	})
}
