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

package reporter

import (
	"context"
	"fmt"
	"sort"

	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-api/pkg/protocol/reporterplugin/v1alpha1"
	"github.com/kubewharf/katalyst-core/pkg/client"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
)

// Manager indicates the way that resources are updated, and it
// is also responsible for other
type Manager interface {
	// PushContents gets ReportField list from report manager, the reporter implementation
	// should be responsible for assembling and updating the specific object.
	PushContents(ctx context.Context, responses map[string]*v1alpha1.GetReportContentResponse) error

	// Run starts all the updaters registered in this Manager.
	Run(ctx context.Context)
}

type managerImpl struct {
	conf *config.Configuration

	// reporters are the map of gvk to reporter
	reporters map[v1.GroupVersionKind]Reporter
}

// NewReporterManager is to create a reporter manager
func NewReporterManager(genericClient *client.GenericClientSet, metaServer *metaserver.MetaServer,
	emitter metrics.MetricEmitter, conf *config.Configuration) (Manager, error) {
	r := &managerImpl{
		reporters: make(map[v1.GroupVersionKind]Reporter),
		conf:      conf,
	}

	err := r.getReporter(genericClient, metaServer, emitter, conf, GetRegisteredInitializers())
	if err != nil {
		return nil, err
	}
	return r, nil
}

func (r *managerImpl) PushContents(ctx context.Context, responses map[string]*v1alpha1.GetReportContentResponse) error {
	var errList []error

	// aggregate all plugin response by gvk
	reportFieldsByGVK := aggregateReportFieldsByGVK(responses)

	// it will update all fields by updater with same gvk
	for gvk, fields := range reportFieldsByGVK {
		u, ok := r.reporters[gvk]
		if !ok || u == nil {
			return fmt.Errorf("reporter of gvk %s not found", gvk)
		}

		err := u.Update(ctx, fields)
		if err != nil {
			errList = append(errList, fmt.Errorf("reporter %s report failed with error: %s", gvk, err))
		}
	}

	return errors.NewAggregate(errList)
}

func (r *managerImpl) Run(ctx context.Context) {
	for _, u := range r.reporters {
		go u.Run(ctx)
	}
	<-ctx.Done()
}

func (r *managerImpl) getReporter(genericClient *client.GenericClientSet, metaServer *metaserver.MetaServer,
	emitter metrics.MetricEmitter, conf *config.Configuration, initFunc map[v1.GroupVersionKind]InitFunc) error {
	var errList []error
	for gvk, f := range initFunc {
		reporter, err := f(genericClient, metaServer, emitter, conf)
		if err != nil {
			errList = append(errList, err)
			continue
		}
		r.reporters[gvk] = reporter
	}

	if len(errList) > 0 {
		return errors.NewAggregate(errList)
	}

	return nil
}

// aggregateReportFieldsByGVK aggregate all report field of plugins' response by its groupVersionKind
// because different plugins may be responsible for one groupVersionKind.
func aggregateReportFieldsByGVK(reportResponses map[string]*v1alpha1.GetReportContentResponse) map[v1.GroupVersionKind][]*v1alpha1.ReportField {
	reportFields := make(map[v1.GroupVersionKind][]*v1alpha1.ReportField)

	// sort reportResponses by name to make sure aggregated result is in order
	reporterNameList := make([]string, 0, len(reportResponses))
	for name := range reportResponses {
		reporterNameList = append(reporterNameList, name)
	}
	sort.SliceStable(reporterNameList, func(i, j int) bool {
		return reporterNameList[i] > reporterNameList[j]
	})

	for _, name := range reporterNameList {
		response := reportResponses[name]
		if response == nil {
			continue
		}

		for _, c := range response.GetContent() {
			if c == nil {
				continue
			}

			gvk := c.GetGroupVersionKind()
			if gvk == nil {
				continue
			}

			if _, ok := reportFields[*gvk]; !ok {
				reportFields[*gvk] = make([]*v1alpha1.ReportField, 0)
			}

			reportFields[*gvk] = append(reportFields[*gvk], c.GetField()...)
		}
	}

	return reportFields
}
