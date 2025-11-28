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

package decorator

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/resource"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/poweraware/spec"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

func NewAssemblerDiscountDecorator(inner headroomassembler.HeadroomAssembler,
	conf *config.Configuration, extraConf interface{},
	metaReader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) headroomassembler.HeadroomAssembler {
	discounts := map[spec.PowerAlert]float64{
		spec.PowerAlertP1: conf.CPUHeadroomPowerDiscountP1,
		spec.PowerAlertP2: conf.CPUHeadroomPowerDiscountP2,
		spec.PowerAlertP3: conf.CPUHeadroomPowerDiscountP3,
	}

	return &discountDecorator{
		inner: inner,
		discounter: &nodeAnnotationDiscountGetter{
			specFetcher: spec.NewFetcher(metaServer.NodeFetcher, conf.PowerAwarePluginConfiguration.AnnotationKeyPrefix),
			conf:        conf,
			discounts:   discounts,
		},
	}
}

type DiscountGetter interface {
	GetDiscount() (float64, error)
}

type nodeAnnotationDiscountGetter struct {
	specFetcher spec.SpecFetcher
	conf        *config.Configuration

	discounts map[spec.PowerAlert]float64
}

func getDiscountByLevel(level spec.PowerAlert, discounts map[spec.PowerAlert]float64) float64 {
	if len(level) == 0 {
		return 1.0
	}

	if value, ok := discounts[level]; ok {
		return value
	}

	switch level {
	case spec.PowerAlertS0, spec.PowerAlertP0:
		return 0.0
	default:
		return 1.0
	}
}

func (d *nodeAnnotationDiscountGetter) GetDiscount() (float64, error) {
	powerSpec, err := d.specFetcher.GetPowerSpec(context.Background())
	if err != nil {
		return 1.0, errors.Wrap(err, "failed to get discount")
	}

	level := powerSpec.Alert
	return getDiscountByLevel(level, d.discounts), nil
}

type discountDecorator struct {
	inner      headroomassembler.HeadroomAssembler
	discounter DiscountGetter
}

func applyDiscount(cpuQuantity resource.Quantity, discount float64) resource.Quantity {
	milliValue := cpuQuantity.MilliValue()
	newMilliValue := int64(float64(milliValue) * discount)
	newQtyViaMilli := resource.NewMilliQuantity(newMilliValue, resource.DecimalSI)
	return *newQtyViaMilli
}

func (d *discountDecorator) GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error) {
	currentDiscount, err := d.discounter.GetDiscount()
	if err != nil {
		general.Warningf("unable to determine current discount; apply no discount instead")
		return d.inner.GetHeadroom()
	}

	headroom, numaHeadrooms, err := d.inner.GetHeadroom()
	if err == nil && currentDiscount < 1.0 {
		headroom = applyDiscount(headroom, currentDiscount)
		for numa := range numaHeadrooms {
			numaHeadrooms[numa] = applyDiscount(numaHeadrooms[numa], currentDiscount)
		}
	}
	return headroom, numaHeadrooms, err
}
