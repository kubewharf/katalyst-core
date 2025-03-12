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
	"sort"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/errors"
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
)

type nicManagerImpl struct {
	sync.RWMutex

	emitter     metrics.MetricEmitter
	nics        *NICs
	enabledNICs []machine.InterfaceInfo
	checkers    map[string]checker.NICHealthChecker
}

func NewNICManager(metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter, conf *config.Configuration) (NICManager, error) {
	// it is incorrect to reserve bandwidth on those disabled NICs.
	// we only count active NICs as available network devices and allocate bandwidth on them
	enabledNICs := filterNICsByAvailability(metaServer.KatalystMachineInfo.ExtraNetworkInfo.Interface)
	if len(enabledNICs) != 0 {
		// the NICs should be in order by interface name so that we can adopt specific policies for bandwidth reservation or allocation
		// e.g. reserve bandwidth for high-priority tasks on the first NIC
		sort.SliceStable(enabledNICs, func(i, j int) bool {
			return enabledNICs[i].Iface < enabledNICs[j].Iface
		})
	} else {
		general.Infof("no valid nics on this node")
	}

	checkers, err := initHealthCheckers(checker.DefaultRegistry, conf.NICHealthCheckers)
	if err != nil {
		return nil, err
	}

	nics, err := checkNICs(emitter, checkers, enabledNICs)
	if err != nil {
		return nil, err
	}

	return &nicManagerImpl{
		emitter:     emitter,
		enabledNICs: enabledNICs,
		nics:        nics,
		checkers:    checkers,
	}, nil
}

func (n *nicManagerImpl) GetNICs() NICs {
	n.RLock()
	defer n.RUnlock()
	return *n.nics
}

func (n *nicManagerImpl) Run(ctx context.Context) {
	go wait.UntilWithContext(ctx, n.updateNICs, 1*time.Minute)
}

func (n *nicManagerImpl) updateNICs(_ context.Context) {
	general.Infof("start to update nics")
	if len(n.enabledNICs) == 0 || len(n.checkers) == 0 {
		general.Warningf("no enabled nics or checkers")
		return
	}

	nics, err := checkNICs(n.emitter, n.checkers, n.enabledNICs)
	if err != nil {
		general.Errorf("failed to check nics: %v", err)
		return
	}

	n.Lock()
	defer n.Unlock()
	n.nics = nics
	general.Infof("update nics successfully")
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

func filterNICsByAvailability(nics []machine.InterfaceInfo) []machine.InterfaceInfo {
	filteredNICs := make([]machine.InterfaceInfo, 0, len(nics))
	for _, nic := range nics {
		if !nic.Enable {
			general.Warningf("nic: %s isn't enabled", nic.Iface)
			continue
		} else if nic.Addr == nil || (len(nic.Addr.IPV4) == 0 && len(nic.Addr.IPV6) == 0) {
			general.Warningf("nic: %s doesn't have IP address", nic.Iface)
			continue
		}

		filteredNICs = append(filteredNICs, nic)
	}

	if len(filteredNICs) == 0 {
		general.InfoS("nic list returned by filterNICsByAvailability is empty")
	}

	return filteredNICs
}

func checkNICs(emitter metrics.MetricEmitter, checkers map[string]checker.NICHealthChecker, nics []machine.InterfaceInfo) (*NICs, error) {
	var (
		healthyNICs   []machine.InterfaceInfo
		unHealthyNICs []machine.InterfaceInfo
		errList       []error
	)

	for _, nic := range nics {
		var unHealthCheckers []string
		err := machine.DoNetNS(nic.NSName, nic.NSAbsolutePath, func(sysFsDir string) error {
			for name, healthChecker := range checkers {
				health, err := healthChecker.CheckHealth(nic)
				if err != nil {
					general.Warningf("NIC %s health check '%s' error: %v", nic.Iface, name, err)
					return err
				}

				if !health {
					unHealthCheckers = append(unHealthCheckers, name)
					break
				}
			}
			return nil
		})
		if err != nil {
			errList = append(errList, err)
			continue
		}

		if len(unHealthCheckers) == 0 {
			general.Infof("NIC %s passed all health checks", nic.Iface)
			healthyNICs = append(healthyNICs, nic)
		} else {
			general.Warningf("NIC %s failed health check: %v", nic.Iface, unHealthCheckers)
			unHealthyNICs = append(unHealthyNICs, nic)
			for _, name := range unHealthCheckers {
				_ = emitter.StoreInt64(metricsNameNICUnhealthyState, 1, metrics.MetricTypeNameRaw,
					metrics.MetricTag{Key: "nic", Val: nic.Iface},
					metrics.MetricTag{Key: "checker", Val: name},
				)
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
