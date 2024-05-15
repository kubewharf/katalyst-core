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
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
)

func TestServiceSinglePortSDManage(t *testing.T) {
	t.Parallel()

	server1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	server2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer func() {
		server1.Close()
		server2.Close()
	}()
	u1, err := url.Parse(server1.URL)
	assert.NoError(t, err)
	port1, err := strconv.Atoi(u1.Port())
	assert.NoError(t, err)

	u2, err := url.Parse(server2.URL)
	assert.NoError(t, err)
	port2, err := strconv.Atoi(u2.Port())
	assert.NoError(t, err)

	service := &v1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns-t",
			Name:      "name-t",
		},
	}

	endpoint := &v1.Endpoints{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns-t",
			Name:      "name-t",
		},
		Subsets: []v1.EndpointSubset{
			{
				Addresses: []v1.EndpointAddress{{
					IP: u1.Hostname(),
				}},
				Ports: []v1.EndpointPort{
					{
						Name:     "port-t",
						Port:     int32(port1),
						Protocol: v1.ProtocolTCP,
					},
				},
			},
			{
				Addresses: []v1.EndpointAddress{{
					IP: u2.Hostname(),
				}},
				Ports: []v1.EndpointPort{
					{
						Name:     "port-t",
						Port:     int32(port2),
						Protocol: v1.ProtocolTCP,
					},
				},
			},
		},
	}

	ctx := context.Background()
	genericCtx, err := katalystbase.GenerateFakeGenericContext([]runtime.Object{service, endpoint}, []runtime.Object{}, []runtime.Object{})
	assert.NoError(t, err)

	conf := &generic.ServiceDiscoveryConf{
		Name: ServiceDiscoveryServiceSinglePort,
		ServiceSinglePortSDConf: &generic.ServiceSinglePortSDConf{
			Namespace: "ns-t",
			Name:      "name-t",
			PortName:  "port-t",
		},
	}
	m, err := GetSDManager(ctx, genericCtx, conf)
	assert.NoError(t, err)

	genericCtx.KubeInformerFactory.Start(ctx.Done())
	err = m.Run()
	assert.NoError(t, err)

	endpoints, err := m.GetEndpoints()
	assert.NoError(t, err)
	assert.ElementsMatch(t, []string{
		fmt.Sprintf("[%v]:%v", u1.Hostname(), port1),
		fmt.Sprintf("[%v]:%v", u2.Hostname(), port2),
	},
		endpoints)
}
