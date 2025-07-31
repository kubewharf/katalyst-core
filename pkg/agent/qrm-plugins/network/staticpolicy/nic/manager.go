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

package nic

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/network/staticpolicy/nic/checker"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	metricsNameNICUnhealthyState = "nic_unhealthy_state"

	nicHealthCheckTime     = 3
	nicHealthCheckInterval = 5 * time.Second
)

type nicManagerImpl struct {
	sync.RWMutex

	conf                   *config.Configuration
	emitter                metrics.MetricEmitter
	nics                   *NICs
	defaultAllocatableNICs []machine.InterfaceInfo
	checkers               map[string]checker.NICHealthChecker
	nicHealthCheckTime     int
	nicHealthCheckInterval time.Duration
}

func NewNICManager(metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration) (NICManager, error) {
	defaultAllocatableNICs := metaServer.ExtraNetworkInfo.GetAllocatableNICs(conf.MachineInfoConfiguration)
	checkers, err := initHealthCheckers(checker.DefaultRegistry, conf.NICHealthCheckers)
	if err != nil {
		return nil, err
	}

	return &nicManagerImpl{
		conf:                   conf,
		emitter:                emitter,
		defaultAllocatableNICs: defaultAllocatableNICs,
		// we initialize nics with defaultAllocatableNICs, since we don't want to miss any nics
		nics: &NICs{
			HealthyNICs: defaultAllocatableNICs,
		},
		checkers:               checkers,
		nicHealthCheckTime:     nicHealthCheckTime,
		nicHealthCheckInterval: nicHealthCheckInterval,
	}, nil
}

func (n *nicManagerImpl) GetNICs() NICs {
	n.RLock()
	defer n.RUnlock()
	return *n.nics
}

func (n *nicManagerImpl) Run(ctx context.Context) {
	n.updateNICs(ctx)
	go wait.UntilWithContext(ctx, n.updateNICs, 1*time.Minute)
}

func (n *nicManagerImpl) updateNICs(_ context.Context) {
	general.Infof("start to update nics")
	if len(n.defaultAllocatableNICs) == 0 || len(n.checkers) == 0 {
		general.Warningf("no enabled nics or checkers")
		return
	}

	nics, err := n.checkNICs(n.defaultAllocatableNICs)
	if err != nil {
		general.Errorf("failed to check nics: %v", err)
		return
	}

	n.Lock()
	defer n.Unlock()
	n.nics = nics
	general.Infof("update nics successfully %#v", *nics)
}

func initHealthCheckers(registry checker.Registry, enableCheckers []string) (map[string]checker.NICHealthChecker, error) {
	checkers := make(map[string]checker.NICHealthChecker)
	for name, factory := range registry {
		if !general.IsNameEnabled(name, nil, enableCheckers) {
			general.Warningf("%q is disabled", name)
			continue
		}

		c, err := factory()
		if err != nil {
			general.Errorf("failed to initialize NIC health checker %s: %v", name, err)
			return nil, err
		}

		checkers[name] = c
		general.Infof("successfully registered NIC health checker: %s", name)
	}

	return checkers, nil
}

func (n *nicManagerImpl) checkNICs(nics []machine.InterfaceInfo) (*NICs, error) {
	var (
		healthyNICs   []machine.InterfaceInfo
		unHealthyNICs []machine.InterfaceInfo
		errList       []error
	)

	for _, nic := range nics {
		successCheckers := sets.NewString()
		err := machine.DoNetNS(nic.NSName, n.conf.NetNSDirAbsPath, func(sysFsDir string) error {
			for i := 0; i <= n.nicHealthCheckTime; i++ {
				for name, healthChecker := range n.checkers {
					health, err := healthChecker.CheckHealth(nic)
					if err != nil {
						general.Warningf("NIC %s health check '%s' error: %v", nic.Name, name, err)
						continue
					}

					if health {
						successCheckers.Insert(name)
					}
				}
				time.Sleep(n.nicHealthCheckInterval)
			}
			return nil
		})
		if err != nil {
			errList = append(errList, err)
			continue
		}

		if successCheckers.Len() == len(n.checkers) {
			general.Infof("NIC %s passed all health checks", nic.Name)
			healthyNICs = append(healthyNICs, nic)
		} else {
			general.Warningf("NIC %s health check partitial success: %v, checkers: %v", nic.Name, successCheckers.List(), n.checkers)
			unHealthyNICs = append(unHealthyNICs, nic)
			for name := range n.checkers {
				if !successCheckers.Has(name) {
					_ = n.emitter.StoreInt64(metricsNameNICUnhealthyState, 1, metrics.MetricTypeNameRaw,
						metrics.MetricTag{Key: "nic", Val: nic.Name},
						metrics.MetricTag{Key: "checker", Val: name},
					)
				}
			}
		}
	}

	if len(errList) > 0 {
		return nil, errors.NewAggregate(errList)
	}

	return &NICs{
		HealthyNICs:   healthyNICs,
		UnhealthyNICs: unHealthyNICs,
	}, nil
}
