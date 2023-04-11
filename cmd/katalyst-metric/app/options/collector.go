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

package options

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/labels"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/metric"
)

const (
	CustomMetricsCollectorNamePrometheus = "prometheus-collector"
)

// CollectorOptions holds the configurations for katalyst metrics collectors.
type CollectorOptions struct {
	PodLabelSelector  string
	NodeLabelSelector string

	ShardNum        int
	CollectorName   string
	CollectInterval time.Duration
}

// NewCollectorOptions creates a new CollectorOptions with a default config.
func NewCollectorOptions() *CollectorOptions {
	return &CollectorOptions{
		ShardNum:        1,
		CollectorName:   CustomMetricsCollectorNamePrometheus,
		CollectInterval: 3 * time.Second,

		PodLabelSelector:  labels.Nothing().String(),
		NodeLabelSelector: labels.Everything().String(),
	}
}

// AddFlags adds flags  to the specified FlagSet.
func (o *CollectorOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("metric-collector")

	fs.StringVar(&o.PodLabelSelector, "collector-pod-selector", o.PodLabelSelector, fmt.Sprintf(
		"selector matches up with the pods that collector will try to scrape"))
	fs.StringVar(&o.NodeLabelSelector, "collector-node-selector", o.NodeLabelSelector, fmt.Sprintf(
		"selector matches up with the nodes that collector will try to scrape"))

	fs.StringVar(&o.CollectorName, "collector-name", o.CollectorName, fmt.Sprintf(
		"which collector implementation will be started"))
	fs.IntVar(&o.ShardNum, "collector-shared", o.ShardNum, fmt.Sprintf(
		"the number of shardings this collector implementation will be responsible for"))
	fs.DurationVar(&o.CollectInterval, "collector-interval", o.CollectInterval, fmt.Sprintf(
		"the interval between two collecting actions"))

}

// ApplyTo fills up config with options
func (o *CollectorOptions) ApplyTo(c *metric.CollectorConfiguration) error {
	c.ShardNum = o.ShardNum
	c.CollectorName = o.CollectorName
	c.SyncInterval = o.CollectInterval

	podSelector, err := labels.Parse(o.PodLabelSelector)
	if err != nil {
		return err
	}
	c.PodSelector = podSelector

	nodeSelector, err := labels.Parse(o.NodeLabelSelector)
	if err != nil {
		return err
	}
	c.NodeSelector = nodeSelector

	return nil
}

func (o *CollectorOptions) Config() (*metric.CollectorConfiguration, error) {
	c := metric.NewCollectorConfiguration()
	if err := o.ApplyTo(c); err != nil {
		return nil, err
	}
	return c, nil
}
