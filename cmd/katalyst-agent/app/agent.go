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
	"sync"
	"time"

	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
)

const healthzNameLockingFileAcquired = "LockingFileReady"

const (
	metricsNameLockingFailed = "get_lock_failed"
	metricsNameAgentStarted  = "agent_started"
)

// Run starts common and uniformed agent components here, and starts other
// specific components in other separate repos (with common components as
// dependencies)
func Run(conf *config.Configuration, clientSet *client.GenericClientSet, genericOptions ...katalystbase.GenericOptions) error {
	// Set up signals so that we handle the first shutdown signal gracefully.
	ctx := process.SetupSignalHandler()

	baseCtx, err := katalystbase.NewGenericContext(clientSet, "", nil, AgentsDisabledByDefault,
		conf.GenericConfiguration, consts.KatalystComponentAgent, conf.DynamicAgentConfiguration)
	if err != nil {
		return err
	}

	genericCtx, err := agent.NewGenericContext(baseCtx, conf)
	if err != nil {
		return err
	}

	for _, genericOption := range genericOptions {
		genericOption(genericCtx)
	}

	lock := acquireLock(genericCtx, conf)
	defer func() {
		// if the process panic in other place and the defer function isn't executed,
		// OS will help to unlock. So the next process till get the lock successfully.
		general.ReleaseUniqueLock(lock)

		// wait async log sync to disk
		time.Sleep(1 * time.Second)
	}()

	return startAgent(ctx, genericCtx, conf, GetAgentInitializers())
}

// startAgent is used to initialize and start each component in katalyst-agent
func startAgent(ctx context.Context, genericCtx *agent.GenericContext,
	conf *config.Configuration, agents map[string]AgentStarter,
) error {
	componentMap := make(map[string]agent.Component)
	monitorAgentStart(genericCtx)
	for agentName, starter := range agents {
		if !genericCtx.IsEnabled(agentName, conf.Agents) {
			klog.Warningf("%q is disabled", agentName)
			continue
		}

		klog.Infof("initializing %q", agentName)
		needToRun, component, err := starter.Init(genericCtx, conf, starter.ExtraConf, agentName)
		if err != nil {
			klog.Errorf("Error initializing %q", agentName)
			return err
		} else if !needToRun {
			klog.Warningf("skip to call running functions %q", agentName)
			continue
		}

		componentMap[agentName] = component
		klog.Infof("needToRun %q", agentName)
	}

	// initialize dynamic config first before components run.
	err := genericCtx.InitializeConfig(ctx)
	if err != nil {
		return fmt.Errorf("initialize dynamic config failed: %v", err)
	}

	wg := sync.WaitGroup{}

	// start generic ctx first
	wg.Add(1)
	go func() {
		defer wg.Done()
		genericCtx.Run(ctx)
	}()

	// watch auth configuration
	err = genericCtx.MetaServer.AddConfigWatcher(crd.AuthConfigurationGVR)
	if err != nil {
		return fmt.Errorf("add authconfiguration watcher failed")
	}

	// start all component and make sure them can be stopped completely
	for agentName, component := range componentMap {
		wg.Add(1)
		runnable := component
		go func() {
			defer wg.Done()
			runnable.Run(ctx)
			klog.Infof("component %q stopped", agentName)
		}()

		klog.Infof("started %q", agentName)
	}

	wg.Wait()
	return nil
}

func monitorAgentStart(genericCtx *agent.GenericContext) {
	_ = genericCtx.EmitterPool.GetDefaultMetricsEmitter().StoreInt64(metricsNameAgentStarted, 1, metrics.MetricTypeNameCount)
}

// acquireLock makes sure only one process can handle socket files;
// any process that wants to enter main loop, should acquire file lock firstly.
func acquireLock(genericCtx *agent.GenericContext, conf *config.Configuration) *general.Flock {
	// register a not-ready state for lock-acquiring when we starts
	general.RegisterHeartbeatCheck(healthzNameLockingFileAcquired, 0, general.HealthzCheckStateNotReady, 0)

	// set a ready state for lock-acquiring when we acquire locking successfully
	defer func() {
		_ = general.UpdateHealthzState(healthzNameLockingFileAcquired, general.HealthzCheckStateReady, "")
	}()

	for {
		lock, err := general.GetUniqueLock(fmt.Sprintf("%s-%s", conf.LockFileName, conf.NodeName))
		if err != nil {
			_ = genericCtx.EmitterPool.GetDefaultMetricsEmitter().StoreInt64(metricsNameLockingFailed, 1, metrics.MetricTypeNameRaw)
			// if waiting is enabled, we will always wait until lock has been obtained successfully;
			if conf.LockWaitingEnabled {
				continue
			}
			panic(err)
		} else {
			return lock
		}
	}
}
