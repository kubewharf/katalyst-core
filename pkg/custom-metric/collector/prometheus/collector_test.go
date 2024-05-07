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

package prometheus

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	katalystbase "github.com/kubewharf/katalyst-core/cmd/base"
	"github.com/kubewharf/katalyst-core/pkg/config/metric"
	metricconf "github.com/kubewharf/katalyst-core/pkg/config/metric"
	"github.com/kubewharf/katalyst-core/pkg/custom-metric/store/local"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/native"
)

var (
	credentialPath = "/tmp/katalyst-ut/credential"
	username       = "katalyst"
	password       = "password"
)

func setupCredential(credentialPath string) error {
	err := general.EnsureDirectory(credentialPath)
	if err != nil {
		return err
	}

	usernameFile, err := os.OpenFile(path.Join(credentialPath, fileNameUsername), os.O_WRONLY|os.O_CREATE, 0o755)
	if err != nil {
		return err
	}
	defer usernameFile.Close()
	usernameWriter := bufio.NewWriter(usernameFile)
	_, _ = usernameWriter.WriteString(username)
	_ = usernameWriter.Flush()

	passwordFile, err := os.OpenFile(path.Join(credentialPath, fileNamePassword), os.O_WRONLY|os.O_CREATE, 0o755)
	if err != nil {
		return err
	}
	defer passwordFile.Close()
	passwordWriter := bufio.NewWriter(passwordFile)
	_, _ = passwordWriter.WriteString(password)
	_ = passwordWriter.Flush()

	return nil
}

func TestPrometheusAddRequests(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(``))
	}))
	defer server.Close()

	err2 := setupCredential(credentialPath)
	assert.NoError(t, err2)

	baseCtx, _ := katalystbase.GenerateFakeGenericContext(nil, nil, nil, nil)
	genericConf := &metricconf.GenericMetricConfiguration{}
	collectConf := &metric.CollectorConfiguration{
		PodSelector:    labels.NewSelector(),
		NodeSelector:   labels.NewSelector(),
		CredentialPath: credentialPath,
	}
	storeConf := &metricconf.StoreConfiguration{}
	localStore, _ := local.NewLocalMemoryMetricStore(ctx, baseCtx, genericConf, storeConf)

	promCollector, err := NewPrometheusCollector(ctx, baseCtx, genericConf, collectConf, localStore)
	assert.NoError(t, err)
	promCollector.(*prometheusCollector).client, _ = newPrometheusClient()
	assert.Equal(t, promCollector.(*prometheusCollector).username, username)
	assert.Equal(t, promCollector.(*prometheusCollector).password, password)

	hostAndPort := strings.Split(strings.TrimPrefix(server.URL, "http://"), ":")
	assert.Equal(t, 2, len(hostAndPort))
	port, _ := strconv.Atoi(hostAndPort[1])
	promCollector.(*prometheusCollector).addRequest(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns1",
			Name:      "name1",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Ports: []v1.ContainerPort{
						{
							Name:     native.ContainerMetricPortName,
							HostPort: int32(port),
						},
					},
				},
			},
		},
		Status: v1.PodStatus{
			HostIP: hostAndPort[0],
		},
	})
	assert.Equal(t, 1, len(promCollector.(*prometheusCollector).scrapes))

	promCollector.(*prometheusCollector).addRequest(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "ns1",
			Name:      "name1",
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Ports: []v1.ContainerPort{
						{
							Name:     native.ContainerMetricPortName,
							HostPort: 11,
						},
					},
				},
			},
		},
		Status: v1.PodStatus{
			HostIP: "not-exist",
		},
	})
	assert.Equal(t, 1, len(promCollector.(*prometheusCollector).scrapes))
}
