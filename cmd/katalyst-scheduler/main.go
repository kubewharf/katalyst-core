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

package main

import (
	"os"

	"github.com/spf13/cobra"
	"k8s.io/component-base/logs"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-scheduler/app"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/plugins/nodeovercommitment"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/plugins/noderesourcetopology"
	"github.com/kubewharf/katalyst-core/pkg/scheduler/plugins/qosawarenoderesources"

	// Ensure scheme package is initialized.
	_ "github.com/kubewharf/katalyst-api/pkg/apis/scheduling/config/scheme"
)

func main() {
	// Register custom plugins to the scheduler framework.
	// Later they can consist of scheduler profile(s) and hence
	// used by various kinds of workloads.
	command := app.NewSchedulerCommand(
		app.WithPlugin(qosawarenoderesources.FitName, qosawarenoderesources.NewFit),
		app.WithPlugin(qosawarenoderesources.BalancedAllocationName, qosawarenoderesources.NewBalancedAllocation),
		app.WithPlugin(noderesourcetopology.TopologyMatchName, noderesourcetopology.New),
		app.WithPlugin(nodeovercommitment.Name, nodeovercommitment.New),
	)

	if err := runCommand(command); err != nil {
		os.Exit(1)
	}
}

func runCommand(cmd *cobra.Command) error {
	// todo: once we switch everything over to Cobra commands, we can go back to calling
	// 	utilflag.InitFlags() (by removing its pflag.Parse() call). For now, we have to set the
	// 	normalize func and add the go flag set by hand.
	logs.InitLogs()
	defer logs.FlushLogs()

	return cmd.Execute()
}
