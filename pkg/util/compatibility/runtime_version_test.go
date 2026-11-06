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

package compatibility

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeRuntimeConn struct {
	invoke func(method string) error
}

func (f *fakeRuntimeConn) Invoke(
	_ context.Context,
	method string,
	_, _ interface{},
	_ ...grpc.CallOption,
) error {
	return f.invoke(method)
}

func (f *fakeRuntimeConn) NewStream(
	context.Context,
	*grpc.StreamDesc,
	string,
	...grpc.CallOption,
) (grpc.ClientStream, error) {
	return nil, errors.New("not implemented")
}

func TestRuntimeVersionResolverUsesCachedVersionWithoutProbe(t *testing.T) {
	t.Parallel()

	store := NewVersionStore(t.TempDir())
	resolver := NewRuntimeVersionResolver(&fakeRuntimeConn{
		invoke: func(method string) error {
			if method == RuntimeV1ServicePrefix+"Version" {
				return status.Error(codes.Unimplemented, "v1 unsupported")
			}
			return nil
		},
	}, time.Second, store)
	version, err := resolver.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, RuntimeVersionV1Alpha2, version)

	calls := 0
	resolver = NewRuntimeVersionResolver(&fakeRuntimeConn{
		invoke: func(string) error {
			calls++
			return nil
		},
	}, time.Second, store)
	version, err = resolver.Get(context.Background())
	require.NoError(t, err)
	require.Equal(t, RuntimeVersionV1Alpha2, version)
	require.Zero(t, calls)
}

func TestRuntimeVersionResolverRefreshBypassesCachedVersion(t *testing.T) {
	t.Parallel()

	store := NewVersionStore(t.TempDir())
	resolver := NewRuntimeVersionResolver(&fakeRuntimeConn{
		invoke: func(method string) error {
			if method == RuntimeV1ServicePrefix+"Version" {
				return status.Error(codes.Unimplemented, "v1 unsupported")
			}
			return nil
		},
	}, time.Second, store)
	_, err := resolver.Get(context.Background())
	require.NoError(t, err)

	resolver = NewRuntimeVersionResolver(&fakeRuntimeConn{
		invoke: func(string) error {
			return nil
		},
	}, time.Second, store)
	version, err := resolver.Refresh(context.Background())
	require.NoError(t, err)
	require.Equal(t, RuntimeVersionV1, version)
}
