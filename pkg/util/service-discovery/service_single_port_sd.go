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

package service_discovery

import (
	"context"
	"fmt"
	"net"
	"time"

	v1 "k8s.io/api/core/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog/v2"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func init() {
	RegisterSDManagerInitializers(ServiceDiscoveryServiceSinglePort, NewServiceSinglePortSDManager)
}

const ServiceDiscoveryServiceSinglePort = "service-single-port"

type serviceSinglePortSDManager struct {
	ctx context.Context

	namespace string
	name      string
	portName  string

	svcLister  corelisters.ServiceLister
	epLister   corelisters.EndpointsLister
	syncedFunc []cache.InformerSynced
}

func NewServiceSinglePortSDManager(ctx context.Context, agentCtx *katalystbase.GenericContext,
	conf *generic.ServiceDiscoveryConf) (ServiceDiscoveryManager, error) {
	klog.Infof("%v sd manager enabled with conf: %+v", ServiceDiscoveryServiceSinglePort, conf.ServiceSinglePortSDConf)
	svcInformer := agentCtx.KubeInformerFactory.Core().V1().Services()
	epInformer := agentCtx.KubeInformerFactory.Core().V1().Endpoints()

	s := &serviceSinglePortSDManager{
		ctx: ctx,

		namespace: conf.ServiceSinglePortSDConf.Namespace,
		name:      conf.ServiceSinglePortSDConf.Name,
		portName:  conf.ServiceSinglePortSDConf.PortName,

		epLister:  epInformer.Lister(),
		svcLister: svcInformer.Lister(),
		syncedFunc: []cache.InformerSynced{
			svcInformer.Informer().HasSynced,
			epInformer.Informer().HasSynced,
		},
	}
	return s, nil
}

func (s *serviceSinglePortSDManager) Name() string { return ServiceDiscoveryServiceSinglePort }

func (s *serviceSinglePortSDManager) GetEndpoints() ([]string, error) {
	svc, err := s.svcLister.Services(s.namespace).Get(s.name)
	if err != nil {
		return nil, err
	}

	eps, err := s.epLister.Endpoints(s.namespace).Get(svc.Name)
	if err != nil {
		return nil, err
	}
	if len(eps.Subsets) == 0 {
		return nil, fmt.Errorf("no endpoints available for service %q", svc.Name)
	}

	var res []string
	for i := range eps.Subsets {
		if urls, err := s.getValidAddresses(&eps.Subsets[i]); err != nil {
			klog.Errorf("subsets %v get valid address err: %v", eps.Subsets[i].String(), err)
		} else {
			res = append(res, urls...)
		}
	}
	return res, nil
}

func (s *serviceSinglePortSDManager) Run() error {
	if !cache.WaitForCacheSync(s.ctx.Done(), s.syncedFunc...) {
		return fmt.Errorf("unable to sync caches for podSinglePortSDManager")
	}
	return nil
}

func (s *serviceSinglePortSDManager) getValidAddresses(ss *v1.EndpointSubset) ([]string, error) {
	results := make([]string, 0)
	for i := range ss.Ports {
		if ss.Ports[i].Name == s.portName {
			for _, address := range ss.Addresses {
				url := fmt.Sprintf("[%s]:%d", address.IP, ss.Ports[i].Port)
				if conn, err := net.DialTimeout("tcp", url, time.Second*5); err == nil {
					if conn != nil {
						_ = conn.Close()
					}
					results = append(results, url)
				} else {
					klog.Errorf("dial %v failed: %v", url, err)
				}
			}
		}
	}

	if len(results) == 0 {
		return results, fmt.Errorf("no valid endpoint exists")
	} else {
		return results, nil
	}
}
