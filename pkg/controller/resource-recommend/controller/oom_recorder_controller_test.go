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

package controller

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"k8s.io/client-go/tools/cache"
	cliflag "k8s.io/component-base/cli/flag"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-controller/app/options"
	"github.com/kubewharf/katalyst-core/pkg/config/controller"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func TestOOMRecorderController_Run(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
	}{
		{
			name: "correct start",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.TODO()
			genericConf := &generic.GenericConfiguration{}
			controllerConf := &controller.GenericControllerConfiguration{
				DynamicGVResources: []string{"deployment.v1.apps"},
			}

			fss := &cliflag.NamedFlagSets{}
			resourceRecommenderOptions := options.NewResourceRecommenderOptions()
			resourceRecommenderOptions.AddFlags(fss)
			resourceRecommenderConf := controller.NewResourceRecommenderConfig()
			_ = resourceRecommenderOptions.ApplyTo(resourceRecommenderConf)

			controlCtx, err := katalystbase.GenerateFakeGenericContext(nil)
			assert.NoError(t, err)

			oc, err := NewPodOOMRecorderController(ctx, controlCtx, genericConf, controllerConf, resourceRecommenderConf)
			assert.NoError(t, err)

			controlCtx.StartInformer(ctx)
			go oc.Run()
			synced := cache.WaitForCacheSync(ctx.Done(), oc.syncedFunc...)
			assert.True(t, synced)
			time.Sleep(10 * time.Millisecond)
		})
	}
}
