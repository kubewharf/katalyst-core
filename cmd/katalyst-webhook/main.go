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
	"fmt"
	"os"

	"github.com/spf13/pflag"
	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/cmd/katalyst-webhook/app"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-webhook/app/options"
)

func main() {
	opt := options.NewOptions()
	fss := &cliflag.NamedFlagSets{}
	opt.AddFlags(fss)

	commandLine := pflag.NewFlagSet(os.Args[0], pflag.ExitOnError)
	for _, f := range fss.FlagSets {
		commandLine.AddFlagSet(f)
	}
	if err := commandLine.Parse(os.Args[1:]); err != nil {
		fmt.Printf("parse command error: %v\n", err)
		os.Exit(1)
	}
	if err := app.Run(opt); err != nil {
		fmt.Printf("run command error: %v\n", err)
		os.Exit(1)
	}
}
