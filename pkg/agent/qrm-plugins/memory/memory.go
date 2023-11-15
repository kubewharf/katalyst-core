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

package memory

import (
	"fmt"
	"time"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/handlers/sockmem"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
)

func init() {
	qrm.RegisterMemoryPolicyInitializer(dynamicpolicy.MemoryResourcePluginPolicyNameDynamic, dynamicpolicy.NewDynamicPolicy)
}

// register qrm memory-handlers registered in adapter
func init() {
	var errList []error
	errList = append(errList, periodicalhandler.RegisterPeriodicalHandler(qrm.QRMMemoryPluginPeriodicalHandlerGroupName,
		sockmem.EnableSetSockMemPeriodicalHandlerName, sockmem.SetSockMemLimit, 60*time.Second))

	aggregatedErr := utilerrors.NewAggregate(errList)
	if aggregatedErr != nil {
		panic(fmt.Errorf("initialize adapter memory qrm plugin failed with error: %v", aggregatedErr.Error()))
	}
}
