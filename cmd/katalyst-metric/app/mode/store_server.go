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

package mode

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/local"
)

func StartCustomMetricStoreServer(ctx context.Context, baseCtx *katalystbase.GenericContext, conf *config.Configuration,
	metricStore store.MetricStore,
) (func() error, func() error, error) {
	if metricStore.Name() != local.MetricStoreNameLocalMemory {
		return nil, nil, fmt.Errorf("only local memory store is supported to run as a server")
	}

	s, ok := metricStore.(*local.LocalMemoryMetricStore)
	if !ok {
		return nil, nil, fmt.Errorf("error to transform metric store into local memory store")
	}

	// use the same port as secure-serving as provider
	mux := http.NewServeMux()
	addr := net.JoinHostPort("0.0.0.0", fmt.Sprintf("%v", conf.Adapter.SecureServing.BindPort))
	server := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return func() error {
		klog.Infof("store server listening on %s ...", addr)
		s.Serve(mux)

		klog.Fatal(server.ListenAndServe())
		<-ctx.Done()

		return nil
	}, func() error { return nil }, nil
}
