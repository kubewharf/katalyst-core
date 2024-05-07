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

package app

import (
	"context"
	"fmt"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/pkg/version"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-controller/app/options"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

func Run(opt *options.Options, genericOptions ...katalystbase.GenericOptions) error {
	klog.Infof("Version: %+v", version.Get())

	conf, err := opt.Config()
	if err != nil {
		return err
	}

	clientSet, err := client.BuildGenericClient(conf.GenericConfiguration.ClientConnection, opt.MasterURL,
		opt.KubeConfig, fmt.Sprintf("%v", consts.KatalystComponentController))
	if err != nil {
		return err
	}

	// Set up signals so that we handle the first shutdown signal gracefully.
	ctx := process.SetupSignalHandler()

	controllerCtx, err := katalystbase.NewGenericContext(clientSet, conf.GenericControllerConfiguration.LabelSelector,
		conf.GenericControllerConfiguration.DynamicGVResources, ControllersDisabledByDefault,
		conf.GenericConfiguration, consts.KatalystComponentController, nil)
	if err != nil {
		return err
	}

	for _, genericOption := range genericOptions {
		genericOption(controllerCtx)
	}

	// start controller ctx first
	controllerCtx.Run(ctx)

	startController := func(startCtx context.Context) {
		defer func() {
			klog.Infoln("ready to stop controller")
			startCtx.Done()
		}()

		klog.Infoln("ready to start controller")
		var controllers []string
		controllers, err := startControllers(startCtx, controllerCtx, conf, GetControllerInitializers())
		if err != nil {
			klog.Fatalf("error starting controllers: %v", err)
		}
		controllerCtx.StartInformer(startCtx)

		go wait.Until(func() {
			klog.Infof("heart beating...")

			for _, name := range controllers {
				_ = controllerCtx.EmitterPool.GetDefaultMetricsEmitter().StoreInt64("heart_beating", 1, metrics.MetricTypeNameCount,
					metrics.MetricTag{Key: "component", Val: string(consts.KatalystComponentController)},
					metrics.MetricTag{Key: "controller", Val: name},
				)
			}
		}, 30*time.Second, ctx.Done())

		<-startCtx.Done()
		klog.Infof("controller exiting")
	}

	defer func() {
		klog.Infoln("katalyst controller is stopped.")
	}()

	if !conf.GenericControllerConfiguration.LeaderElection.LeaderElect {
		startController(ctx)
		<-ctx.Done()

		klog.Infof("controller cmd exiting, wait for goroutines exits.")
		return nil
	}

	klog.Infoln("lead election is enabled")
	id, err := os.Hostname()
	if err != nil {
		klog.Error("fail to get hostname")
		return err
	}
	id = id + "_" + string(uuid.NewUUID())

	rl, err := resourcelock.New(conf.GenericControllerConfiguration.LeaderElection.ResourceLock,
		conf.GenericControllerConfiguration.LeaderElection.ResourceNamespace,
		conf.GenericControllerConfiguration.LeaderElection.ResourceName,
		clientSet.KubeClient.CoreV1(),
		clientSet.KubeClient.CoordinationV1(),
		resourcelock.ResourceLockConfig{
			Identity:      id,
			EventRecorder: controllerCtx.BroadcastAdapter.DeprecatedNewLegacyRecorder(string(consts.KatalystComponentController)),
		})
	if err != nil {
		return err
	}

	leaderelection.RunOrDie(ctx, leaderelection.LeaderElectionConfig{
		Lock:          rl,
		LeaseDuration: conf.GenericControllerConfiguration.LeaderElection.LeaseDuration.Duration,
		RenewDeadline: conf.GenericControllerConfiguration.LeaderElection.RenewDeadline.Duration,
		RetryPeriod:   conf.GenericControllerConfiguration.LeaderElection.RetryPeriod.Duration,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: startController,
			OnStoppedLeading: func() {
				klog.Infof("loss leader lock.")
			},
		},
	})

	return fmt.Errorf("lost lease")
}

func startControllers(ctx context.Context, controllerCtx *katalystbase.GenericContext,
	conf *config.Configuration, controllers map[string]ControllerStarter,
) ([]string, error) {
	var enabledControllers []string

	for controllerName, starter := range controllers {
		if !controllerCtx.IsEnabled(controllerName, conf.GenericControllerConfiguration.Controllers) {
			klog.Warningf("%q is disabled", controllerName)
			continue
		}
		enabledControllers = append(enabledControllers, controllerName)

		klog.Infof("Starting %q", controllerName)
		started, err := starter.Starter(ctx, controllerCtx, conf, starter.ExtraConf, controllerName)
		if err != nil {
			klog.Errorf("Error starting %q", controllerName)
			return []string{}, err
		} else if !started {
			klog.Warningf("Skipping %q", controllerName)
			continue
		}

		klog.Infof("Started %q", controllerName)
	}
	return enabledControllers, nil
}
