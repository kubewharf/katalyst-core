---
title: Katalyst Reporter Manager
authors:
  - "csfldf"
reviewers:
  - "waynepeking348"
  - "luomingmeng"
  - "caohe"
creation-date: 2022-04-24
last-updated: 2023-02-23
status: implemented
---

<!-- toc -->

## Table of Contents

- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals](#non-goals)
- [Proposal](#proposal)
    - [User Stories](#user-stories)
        - [Story 1](#story-1)
        - [Story 2](#story-2)
    - [Design Overview [Optional]](#design-overview-optional)
    - [API [Optional]](#api-optional)
    - [Design Details](#design-details)
- [Alternatives](#alternatives)

<!-- toc -->

## Summary

Eviction is usually used as a common back-and-force method, in case that QoS requirements fail to be satisfied. The eviction manager will work as a general framework, and is the only entrance for eviction logic. Different vendors can implement their own eviction strategy based on customized scenario, and the eviction manager will gather those info from all plugins, analyze by sorting and filtering algorithms, and then trigger eviction requests.

## Motivation


### Goals

- Make it easier for vendors or administrators to implement customized eviction strategies.
- Implement a common framework to converge eviction info from multiple eviction plugins.

### Non-Goals

- Replace the original implementation of eviction manager in kubelet.
- Implement a fully functional eviction strategy to cover all scenarios.

## Proposal

### User Story

For a production environment containing pods with multiple QoS levels, there may exist different resources or device vendors, and they usually focus on their customized scenarios. For instance, disk vendors mainly keep an eye on whether contention happens at disk-level, such as iops for disk is beyond threshold, and nic vendors usually care about contention at network interface or protocol stack.

Compared with kubelet eviction manager static threshold strategy, katalyst eviction manager provides more flexible eviction interfaces. Vendors or administrators just need to focus on implementing customized eviction strategies in plugins for pressure detection and picking eviction candidates. There is no need for them to perform eviction requests or mark pressure conditions in Node or CNR, katalyst eviction manager will do it as a coordinator. Without a coordinator, each plugin may choose pods from its perspective, thus evicting too many pods. Katalyst eviction manager will analyze candidates from all plugins by sorting and filtering algorithms and perform eviction requests under control of throttle algorithm, thus reducing the disturbance.

### Design Overview
<div align="center">
  <picture>
    <img src="/docs/imgs/eviction-manager-overview.png" width=80% title="Katalyst Overview" loading="eager" />
  </picture>
</div>

For architecture overview, the system mainly contains two modules: eviction manager and eviction plugin.
- Eviction Manager is a coordinator, and communicates with multiple Eviction Plugins. It receives pressure conditions and eviction candidates from each plugin, and makes the final eviction decision based on sorting and filtering algorithms.
- Eviction Plugins are implemented according to each individual vendor or scenario. Each plugin will only output eviction candidates or resource pressure status based on its own knowledge,  and report those info to Eviction Manager periodically.

### API [Optional]

Eviction Plugin communicates with Eviction Manager with GPRC, and the protobuf is shown as below.
```
type ThresholdMetType int

const (
    NotMet ThresholdMetType = iota 
    SoftMet
    HardMet 
)

type ConditionType int

const (
    NodeCondition = iota
    CNRCondition
)

type Condition struct {
    ConditionType ConditionType
    ConditionName string
    MetCondition  bool
}

type ThresholdMetResponse struct {
    ThresholdValue    float64
    ObservedValue     float64
    ThresholdOperator string
    MetType           ThresholdMetType
    EvictionScode     string         // eg. resource name
    Condition         Condition
}

type GetTopEvictionPodsRequest struct {
    ActivePods    []*v1.Pod
    topN          uint64
}

type GetTopEvictionPodsResponse struct {
    TargetPods []*v1.Pod // length is less than or equal to topN in GetTopEvictionPodsRequest
    GracePeriodSeconds   uint64
}

type EvictPods struct {
    Pod              *v1.Pod
    Reason           string
    GracePeriod      time.Duration
    ForceEvict       bool
}

type GetEvictPodsResponse struct {
    EvictPods []*EvictPod
    Condition        Condition
}

func ThresholdMet(Empty) (ThresholdMetResponse, error)
func GetTopEvictionPods(GetTopEvictionPodsRequest) (GetTopEvictionPodsResponse, error) 
func GetEvictPods(Empty) (GetEvictPodsResponse, error)
```

Based on the API, the workflow is as below.
- Eviction Manager periodically calls the ThresholdMet function of each Eviction Plugin through endpoint to get pressure condition status, and filters out the returned values if NotMet. After comparing the smoothed pressure contention with the target threshold, the manager will update pressure conditions both for Node and CNR. If hard threshold is met, eviction manager calls GetTopEvictionPods function of corresponding plugin to get eviction candidates.
- Eviction Manager also periodically calls the GetEvictPods function of each Eviction Plugin to get eviction candidates explicitly. Those candidates include forced ones and soft ones, and the former means the manager should trigger eviction immediately, while the latter means manager should choose a selected set of pods to evict.
- Eviction Manager will then aggregate all candidates, perform filtering, sorting, and rate-limiting logic, and finally send eviction requests for all selected pods.

### Design Details

In this part, we will introduce the detailed responsibility for Eviction Manager, along with embedded eviction plugins in katalyst.

#### Eviction Manager

- Plugin Manager is responsible for the registration process, and constructs the endpoint for each plugin.
- Plugin Endpoints maintain the endpoint info for each plugin, including client, descriptions and so on.
- Launcher is the core calculation module. It will communicate with each plugin through GRPC periodically, and perform eviction strategies to exact those pods for eviction.
- Evictor is the utility module to communicate with APIServer. When candidates are finally confirmed, the Launcher will call Evictor to trigger eviction.
- Condition Reporter is used to update pressure conditions for Node and CNR to prevent more pods from scheduling into the same node if resource pressure already exists.

#### Plugins

- Inner eviction plugins only depend on the raw metrics/data, and are implemented and deployed along with eviction manager. For instance, cpu suppression eviction plugin and reclaimed resource over-commit eviction plugin both belong to this type.
- Outer eviction plugins depend on the calculation results of other modules. For instance, load eviction plugin depends on the allocation results of QRM, and memory bandwidth eviction depends on the suppression strategy in SysAdvisor, so these eviction plugins should be implemented out-of-tree.

## Alternatives

- Implement pod eviction in native kubelet eviction manager, but this invades too much into the source codes. Besides,  we must also implement the metrics collecting and analyzing logic in kubelet, and this means we must be bound with a specific metric source. Finally, upgrading the kubelet is too heavy compared with daemonset,  so if we need to add a new or adjust an existing eviction strategy frequently, it is not convenient.
- Implement pod eviction in each plugin without a centralized coordinator. Usually, when resource contention happens, it may cause thundering herds, meaning that more than one plugin decides to trigger pod eviction. And this problem can not be solved if there is no coordinator.


