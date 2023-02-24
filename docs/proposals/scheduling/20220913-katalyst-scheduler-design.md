---
title: Katalyst Scheduler Design
authors:
  - "@caohe"
reviewers:
  - "@waynepeking348"
  - "@csfldf"
  - "@pendoragon"
  - "@Aiden-cn"
  - "@NickrenREN"
creation-date: 2022-09-13
last-updated: 2023-02-01
status: implemented
---

# Katalyst Scheduler Design

## Table of Contents

<!-- toc -->

- [Summary](#summary)
- [Motivation](#motivation)
    - [Goals](#goals)
    - [Non-Goals/Future Work](#non-goalsfuture-work)
- [Proposal](#proposal)
    - [User Stories](#user-stories)
        - [Story 1](#story-1)
        - [Story 2](#story-2)
        - [Story 3](#story-3)
        - [Story 4](#story-4)
    - [Design Overview](#design-overview)
    - [API](#api)
        - [Request Reclaimed Resources](#request-reclaimed-resources)
        - [Report Allocatable Reclaimed Resources](#report-allocatable-reclaimed-resources)
    - [Design Details](#design-details)
        - [Extended Cache](#extended-cache)
        - [Extended Plugins](#extended-plugins)
            - [QoSAwareNodeResourcesFit Plugin](#qosawarenoderesourcesfit-plugin)
            - [QoSAwareNodeResourcesBalancedAllocation Plugin](#qosawarenoderesourcesbalancedallocation-plugin)
        - [Native Plugins](#native-plugins)
        - [Kubelet's Admission Process](#kubelets-admission-process)
- [Non-Functional Design](#non-functional-design)
    - [Performance](#performance)
    - [Observability](#observability)
    - [Extensibility](#extensibility)
    - [Compatibility](#compatibility)
- [Alternatives](#alternatives)
- [References](#references)

<!-- /toc -->

## Summary

In the Katalyst colocation system, the central scheduler is required to support the following capabilities:

- QoS-aware scheduling

- More job scheduling semantics

- Enhanced support for heterogeneous resources

- Ability to perceive more information at the node level

## Motivation

### Goals

- Support **basic** QoS-aware scheduling

    - Support two extended QoS classes: `shared_cores` and `reclaimed_cores`. So resources can be over-committed for offline workloads.

### Non-Goals/Future Work

- Support **advanced** QoS-aware scheduling

    - Support QoS-aware taint scheduling

    - Support QoS-aware preemption

    - Support two other extended QoS classes: `dedicated_cores` and `system_cores`

    - Support QoS enhancement options, such as `numa_binding`

- Support more job scheduling semantics

    - Coscheduling

    - Binpack scheduling

    - Capacity scheduling

    - Job queueing

- Enhanced support for heterogeneous resources

    - GPU-share scheduling

- Ability to perceive more information at the node level

    - Load-aware scheduling

    - Topology-aware scheduling

## Proposal

### User Stories

#### Story 1

When a user configures the amount of resources for an application, the following situations often occur:

- Users often determine the amount of resources to apply for based on the amount of resources consumed during peak
  periods, but online services can have tidal phenomena, which will cause a waste of resources during low peak
  periods.

- Users may incorrectly estimate an application's resource usage.

- To ensure the stability of their applications, users tend to request far more resources than they actually need.

In these cases, resource utilization tends to be low. Therefore, a **QoS-aware scheduling** mechanism is needed.
On one hand, allocated but unused resources should be **reclaimed** and **over-committed** for applications with a `reclaimed_cores` QoS class. On the other hand, it is necessary to ensure the QoS of online applications.

#### Story 2

When a Pod with a `reclaimed_cores` QoS class fails to be scheduled, it may preempt some Pods with other QoS classes, which may affect the QoS of
online applications.

Therefore, a **QoS-aware preemption** mechanism needs to be introduced. Pods with a `reclaimed_cores` QoS class should be restricted
to preempting only Pods with the same QoS class, not online Pods.

#### Story 3

When the interference detection result of a node is abnormal, or the health status of the Katalyst agent on a node is
abnormal, it is necessary to prevent scheduling any more Pods with a `reclaimed_cores` QoS class to the node.

However, the native taint mechanism will prevent Pods of all QoS classes from being scheduled to a particular node,
which may affect the scheduling of online Pods.

Therefore, a **QoS-aware taint scheduling** mechanism needs to be introduced. If a node is marked with an extended
taint whose effect is `NoScheduleForReclaimedTasks`, only Pods with a `reclaimed_cores` QoS class will be prevented from being scheduled to that node.

#### Story 4

In business scenarios such as search, recommendation, and advertising, applications are highly latency-sensitive.
Containers should be assigned exclusive NUMA nodes to avoid interference between services.

Therefore, a **QoS enhancement option** called **`numa_binding`** needs to be introduced. If a Pod specifies
a `dedicated_cores` QoS class and a `numa_binding` enhancement option, it is necessary to use NUMA node as the
unit when scheduling and allocating resources, so that some NUMA nodes will be dedicated to containers in that Pod.

### Design Overview

<div align="center">
  <picture>
    <img src="./scheduler-overall-arch.jpg" width=50% title="Scheduler Overall Architecture" loading="eager" />
  </picture>
</div>

Components related to scheduling:

- **Katalyst Scheduler** is the central scheduler. Features can be extended through the scheduling framework. We will
  implement these plugins:

    - **QoSAwareNodeResourcesFit** plugin filters and scores nodes based on the amount of resources. It is QoS-aware so
      that resources can be over-committed for offline workloads.

    - **QoSAwareNodeResourcesBalancedAllocation** plugin favors nodes with balanced resource allocation rates. It is
      also QoS-aware and can distinguish between online and offline Pods.

    - (Not yet implemented) **QoSAwareTaintToleration** plugin implements a QoS-aware disable-scheduling mechanism based on
      extended taints in CNR.

    - (Not yet implemented) **QoSAwarePreemption** plugin implements a QoS-aware preemption mechanism.

- **Katalyst Agent** is an agent deployed on each node. It contains the following modules:

    - **Reporter** is an out-of-band information reporting framework. It has these functions:

        - It reports the amount of reclaimed resources to CNR based on SysAdvisor's prediction results.

        - It reports Taint to Node or CNR based on Katalyst Agent's health status and interference detection results.

    - **SysAdvisor** is a module that performs algorithmic and policy calculations.

- **Malachite** is an agent that collects extended metrics on each node, including Container/NUMA/Node level metrics.

### API

#### Request Reclaimed Resources

We will introduce some new resource keys:

- `katalyst.kubewharf.io/reclaimed_millicpu` represents the amount of reclaimed CPU resources requested by a container.
  Its unit is milli-core.

- `katalyst.kubewharf.io/reclaimed_memory` represents the amount of reclaimed memory resources requested by a container.
  Its unit is byte.

If a user wants to create a Pod with a `reclaimed_cores` QoS class which requests 4 CPU cores and 8 GiB of memory,
here is a sample configuration file:

```yaml
apiVersion: v1
kind: Pod
metadata:
  annotations:
    "katalyst.kubewharf.io/qos_level": reclaimed_cores
  ...
spec:
  ...
  containers:
  - ...
    resources:
      requests:
        "katalyst.kubewharf.io/reclaimed_millicpu": "4k"
        "katalyst.kubewharf.io/reclaimed_memory": 8Gi
      limits:
        "katalyst.kubewharf.io/reclaimed_millicpu": "4k"
        "katalyst.kubewharf.io/reclaimed_memory": 8Gi
```

#### Report Allocatable Reclaimed Resources

The amount of allocatable reclaimed resources on a node will be reported to CNR status by Katalyst Agent's Reporter
module.

Assuming that a node has 40 reclaimed CPU cores and 100 GiB reclaimed memory, the CNR associated with that node will be:

```yaml
apiVersion: node.katalyst.kubewharf.io/v1alpha1
kind: CustomNodeResource
metadata:
  name: <node-name>
spec:
  ...
status:
  ...
  resourceAllocatable:
    katalyst.kubewharf.io/reclaimed_memory: "107374182400"
    katalyst.kubewharf.io/reclaimed_millicpu: 40k
  resourceCapacity:
    katalyst.kubewharf.io/reclaimed_memory: "107374182400"
    katalyst.kubewharf.io/reclaimed_millicpu: 40k
```

### Design Details

#### Extended Cache

Because the scheduling framework's native cache does not contain information obtained from CNRs, it is necessary to
extend the scheduler's cache to record precomputed information that is aggregated at node level:

- The amount of reclaimed resources per node

    - Allocatable

    - Requested

    - NonZeroRequested

- The set of assumed Pods

Information in the extended cache will be shared by multiple plugins. When the scheduler starts, Pod's and CNR's
events are registered to the informer.

#### Extended Plugins

##### QoSAwareNodeResourcesFit Plugin

<div align="center">
  <picture>
    <img src="./scheduler-fit-plugin.jpg" width=80% title="QoSAwareNodeResourcesFit Plugin" loading="eager" />
  </picture>
</div>

We will implement the following extension points:

- **PreFilter**

  The plugin precomputes the amount of reclaimed resources requested by a Pod, and stores the result in the CycleState.

- **Filter**

  Based on the amount of reclaimed resources requested by the Pod and the amount of reclaimed
  resources available on the node, the plugin determines whether the node's reclaimed resources are sufficient.

- **Score**

  The plugin scores the node based on the scoring strategy configured by the user and the amount of the node's reclaimed resources.
  The following strategies are supported:

    - **LeastAllocated**: The default strategy. The lower the allocation rates of the node's reclaimed resources, the
      higher the score (i.e., the Spread strategy).

    - **MostAllocated**: The higher the allocation rates of the node's reclaimed resources, the higher the score (i.e.,
      the Binpack strategy).

    - **RequestedToCapacityRatio**: The plugin scores nodes based on user-configured curves, which can flexibly support the above strategies.

- **Reserve**

  After the optimal node is selected, the plugin will update the amount of requested reclaimed resources on the node in the extended
  cache.

- **Unreserve**

  If a stage after Reserve fails, the plugin will roll back the extended cache to subtract the amount of resources requested by the
  Pod for that node.

- **EnqueueExtensions**

  The plugin registers the following types of events to improve the efficiency of re-enqueuing Pods that fail to be scheduled:

    - `DELETE` events for Pods

    - `CREATE` events for Nodes

##### QoSAwareNodeResourcesBalancedAllocation Plugin

<div align="center">
  <picture>
    <img src="./scheduler-balanced-plugin.jpg" width=80% title="QoSAwareNodeResourcesBalancedAllocation Plugin" loading="eager" />
  </picture>
</div>

We will implement the following extension point:

- **Score**

  The plugin scores the node based on the degree of balance among the allocation rates of various resources on the node. The lower
  the difference, the higher the score. To be specific, the formula is:

    ```
    score = (1 - std) * MaxNodeScore
    std = root square of Σ((fraction(i)-mean)^2)/len(resources)
    ```

    - For Pods with a `reclaimed_cores` QoS class, the plugin calculates the degree of balance among the allocation rates of
      various **reclaimed** resources on the node.

    - For Pods with other QoS classes, the plugin calculates the degree of balance among the allocation rates of various
      **non-reclaimed** resources on the node.

#### Native Plugins

The native **NodeResourcesFit** plugin filters and scores nodes based on the amount of **non-reclaimed** resources on each
node. Reclaimed resources should not interfere with this process.

We use the NodeResourcesFit plugin's ignoredResourceGroups feature, introduced in K8s v1.19, to make the plugin ignore
resource types prefixed with `katalyst.kubewharf.io` when filtering nodes:

```yaml
apiVersion: kubescheduler.config.k8s.io/v1beta3
kind: KubeSchedulerConfiguration
profiles:
- pluginConfig:
  - name: NodeResourcesFit
    args:
      ignoredResourceGroups:
      - katalyst.kubewharf.io
... 
```

#### Kubelet's Admission Process

When kubelet admits a Pod, it will check if the resources on the node are sufficient. Since K8s
v1.10, kubelet ignores extended resources that are missing from the node's allocatable resources during the admission process.

Therefore, kubelet's admission process will not be affected by reclaimed resources.

```go
func (w *predicateAdmitHandler) Admit(attrs *PodAdmitAttributes) PodAdmitResult {
   ...

   // Remove the requests of the extended resources that are missing in the
   // node info. This is required to support cluster-level resources, which
   // are extended resources unknown to nodes.
   //
   // Caveat: If a pod was manually bound to a node (e.g., static pod) where a
   // node-level extended resource it requires is not found, then kubelet will
   // not fail admission while it should. This issue will be addressed with
   // the Resource Class API in the future.
   podWithoutMissingExtendedResources := removeMissingExtendedResources(admitPod, nodeInfo)

   reasons := generalFilter(podWithoutMissingExtendedResources, nodeInfo)
   ...
} 
```

## Non-Functional Design

### Performance

- By introducing the extended cache, the time complexity is reduced from *O(Containers)* to *O(Nodes)*.

- In the future, we will consider implementing a snapshot-like mechanism based on CycleState, so that read operations on the extended cache can be as lock-free as possible during the scheduling process.

### Observability

The scheduling framework provides some observability mechanisms, such as metrics, logging, tracing, and events.

In addition, we will consider starting an HTTP server in Katalyst Scheduler, providing the following APIs:

- **Cache Dump API**

  This API returns the current state of an extended cache, such as the amount of reclaimed resources on each node, the
  amount of computing power and memory per GPU card on each node, etc.

- **Score API**

  This API allows users to enable the recording of detailed scoring logs. If this feature is enabled, scores given by each plugin will be recorded in the scheduler's logs.

### Extensibility

In addition to the plugin mechanism natively provided by the scheduling framework, there are some other extension points
in the startup part of the scheduler. To be specific, we can extend the scheduling framework itself with some hook code.
For example,

- We can hook the `RunXxxPlugins` method, and do some preprocessing on the method's parameters, such as `NodeInfo` or `Pod`, before
  calling a plugin at a specific extension point.

- We can customize the logic by which the scheduler watches and processes events. That is, some custom actions can be performed when an event of a particular type is watched, such as updating the scheduler's native cache or scheduling queue.

### Compatibility

Compatibility matrix:

<table>
    <tr>
        <td><b>Version of K8s</b></td>
        <td><b>Version of KubeSchedulerConfiguration</b></td>
        <td><b>Related Changes</b></td>
    </tr>
    <tr>
        <td>v1.20</td>
        <td rowspan="2">v1beta1</td>
        <td rowspan="2">-</td>
    </tr>
    <tr>
        <td>v1.21</td>
    </tr>
    <tr>
        <td>v1.22</td>
        <td>v1beta2</td>
        <td>
            The following plugins have been integrated into the NodeResourcesFit plugin:
            <ul>
                <li>NodeResourcesLeastAllocated</li>
                <li>NodeResourcesMostAllocated</li>
                <li>RequestedToCapacityRatio</li>
            </ul>
        </td>
    </tr>
    <tr>
        <td>v1.23</td>
        <td rowspan="2">v1beta3</td>
        <td rowspan="2">-</td>
    </tr>
    <tr>
        <td>v1.24</td>
    </tr>
    <tr>
        <td>v1.25</td>
        <td rowspan="2">v1</td>
        <td rowspan="2">-</td>
    </tr>
    <tr>
        <td>v1.26</td>
    </tr>
</table>

- For K8s v1.22/v1.23/v1.24/v1.25/v1.26 versions, the following configurations can be used (note that the `apiVersion` field needs to be adjusted):

    - **Spread Strategy (Default)**

        ```yaml
        apiVersion: kubescheduler.config.k8s.io/v1beta3
        kind: KubeSchedulerConfiguration
        leaderElection:
          leaderElect: true
          resourceLock: leases
          resourceName: katalyst-scheduler
          resourceNamespace: katalyst-system
        profiles:
        - schedulerName: katalyst-scheduler
          plugins:
            preFilter:
              enabled:
              - name: QoSAwareNodeResourcesFit
            filter:
              enabled:
              - name: QoSAwareNodeResourcesFit
            score:
              enabled:
              - name: QoSAwareNodeResourcesFit
                weight: 4
              - name: QoSAwareNodeResourcesBalancedAllocation
                weight: 1
              disabled:  
              - name: NodeResourcesFit
              - name: NodeResourcesBalancedAllocation
            reserve:
              enabled:
              - name: QoSAwareNodeResourcesFit
          pluginConfig:
          - name: NodeResourcesFit
            args:
              ignoredResourceGroups:
              - katalyst.kubewharf.io
          - name: QoSAwareNodeResourcesFit # optional
            args:
              scoringStrategy:
                type: LeastAllocated
                resources:
                - name: cpu
                  weight: 1
                - name: memory
                  weight: 1
                reclaimedResources:
                - name: "katalyst.kubewharf.io/reclaimed_millicpu"
                  weight: 1
                - name: "katalyst.kubewharf.io/reclaimed_memory"
                  weight: 1  
          - name: QoSAwareNodeResourcesBalancedAllocation # optional
            args:
              resources:
              - name: cpu
                weight: 1
              - name: memory
                weight: 1
              reclaimedResources:
              - name: "katalyst.kubewharf.io/reclaimed_millicpu"
                weight: 1
              - name: "katalyst.kubewharf.io/reclaimed_memory"
                weight: 1  
        ```

    - **Binpack Strategy**

      ```yaml
      apiVersion: kubescheduler.config.k8s.io/v1beta3
      kind: KubeSchedulerConfiguration
      leaderElection:
        leaderElect: true
        resourceLock: leases
        resourceName: katalyst-scheduler
        resourceNamespace: katalyst-system
      profiles:
      - schedulerName: katalyst-scheduler
        plugins:
          preFilter:
            enabled:
            - name: QoSAwareNodeResourcesFit
          filter:
            enabled:
            - name: QoSAwareNodeResourcesFit
          score:
            enabled:
            - name: QoSAwareNodeResourcesFit
              weight: 15
            - name: QoSAwareNodeResourcesBalancedAllocation
              weight: 1
            disabled:  
            - name: NodeResourcesFit
            - name: NodeResourcesBalancedAllocation
          reserve:
            enabled:
            - name: QoSAwareNodeResourcesFit
        pluginConfig:
        - name: NodeResourcesFit
          args:
            ignoredResourceGroups:
            - katalyst.kubewharf.io
        - name: QoSAwareNodeResourcesFit
          args:
            scoringStrategy:
              type: MostAllocated
              resources:
              - name: cpu
                weight: 1
              - name: memory
                weight: 1
              reclaimedResources:
              - name: "katalyst.kubewharf.io/reclaimed_millicpu"
                weight: 1
              - name: "katalyst.kubewharf.io/reclaimed_memory"
                weight: 1  
        - name: QoSAwareNodeResourcesBalancedAllocation # optional
          args:
            resources:
            - name: cpu
              weight: 1
            - name: memory
              weight: 1
            reclaimedResources:
            - name: "katalyst.kubewharf.io/reclaimed_millicpu"
              weight: 1
            - name: "katalyst.kubewharf.io/reclaimed_memory"
              weight: 1  
      ```

    - **Custom Strategy**

      ```yaml
      apiVersion: kubescheduler.config.k8s.io/v1beta3
      kind: KubeSchedulerConfiguration
      leaderElection:
        leaderElect: true
        resourceLock: leases
        resourceName: katalyst-scheduler
        resourceNamespace: katalyst-system
      profiles:
      - schedulerName: katalyst-scheduler
        plugins:
          preFilter:
            enabled:
            - name: QoSAwareNodeResourcesFit
          filter:
            enabled:
            - name: QoSAwareNodeResourcesFit
          score:
            enabled:
            - name: QoSAwareNodeResourcesFit
              weight: 15
            - name: QoSAwareNodeResourcesBalancedAllocation
              weight: 1
            disabled:  
            - name: NodeResourcesFit
            - name: NodeResourcesBalancedAllocation
          reserve:
            enabled:
            - name: QoSAwareNodeResourcesFit
        pluginConfig:
        - name: NodeResourcesFit
          args:
            ignoredResourceGroups:
            - katalyst.kubewharf.io
        - name: QoSAwareNodeResourcesFit
          args:
            scoringStrategy:
              type: RequestedToCapacityRatio
              requestedToCapacityRatio:
                shape:
                - utilization: 0
                  score: 0
                - utilization: 100
                  score: 10
              reclaimedRequestedToCapacityRatio:
                shape:
                - utilization: 0
                  score: 0
                - utilization: 100
                  score: 10
              resources:
              - name: cpu
                weight: 1
              - name: memory
                weight: 1
              reclaimedResources:
              - name: "katalyst.kubewharf.io/reclaimed_millicpu"
                weight: 1
              - name: "katalyst.kubewharf.io/reclaimed_memory"
                weight: 1  
        - name: QoSAwareNodeResourcesBalancedAllocation # optional
          args:
            resources:
            - name: cpu
              weight: 1
            - name: memory
              weight: 1
            reclaimedResources:
            - name: "katalyst.kubewharf.io/reclaimed_millicpu"
              weight: 1
            - name: "katalyst.kubewharf.io/reclaimed_memory"
              weight: 1  
      ```

- For K8s v1.20/v1.21 versions, the following configurations can be used (note that the `apiVersion` field is set to `v1beta1`):

    - **Spread Strategy (Default)**

        ```yaml
        apiVersion: kubescheduler.config.k8s.io/v1beta1
        kind: KubeSchedulerConfiguration
        leaderElection:
          leaderElect: true
          resourceLock: leases
          resourceName: katalyst-scheduler
          resourceNamespace: katalyst-system
        profiles:
        - schedulerName: katalyst-scheduler
          plugins:
            preFilter:
              enabled:
              - name: QoSAwareNodeResourcesFit
            filter:
              enabled:
              - name: QoSAwareNodeResourcesFit
            score:
              enabled:
              - name: QoSAwareNodeResourcesFit
                weight: 4
              - name: QoSAwareNodeResourcesBalancedAllocation
                weight: 1
              disabled:  
              - name: NodeResourcesLeastAllocated
              - name: NodeResourcesBalancedAllocation
            reserve:
              enabled:
              - name: QoSAwareNodeResourcesFit
          pluginConfig:
          - name: NodeResourcesFit
            args:
              ignoredResourceGroups:
              - katalyst.kubewharf.io
          - name: QoSAwareNodeResourcesFit
            args:
              scoringStrategy:
                type: LeastAllocated
                resources:
                - name: cpu
                  weight: 1
                - name: memory
                  weight: 1
                reclaimedResources:
                - name: "katalyst.kubewharf.io/reclaimed_millicpu"
                  weight: 1
                - name: "katalyst.kubewharf.io/reclaimed_memory"
                  weight: 1  
          - name: QoSAwareNodeResourcesBalancedAllocation # optional
            args:
              resources:
              - name: cpu
                weight: 1
              - name: memory
                weight: 1
              reclaimedResources:
              - name: "katalyst.kubewharf.io/reclaimed_millicpu"
                weight: 1
              - name: "katalyst.kubewharf.io/reclaimed_memory"
                weight: 1  
        ```

    - **Binpack Strategy**

      ```yaml
      apiVersion: kubescheduler.config.k8s.io/v1beta1
      kind: KubeSchedulerConfiguration
      leaderElection:
        leaderElect: true
        resourceLock: leases
        resourceName: katalyst-scheduler
        resourceNamespace: katalyst-system
      profiles:
      - schedulerName: katalyst-scheduler
        plugins:
          preFilter:
            enabled:
            - name: QoSAwareNodeResourcesFit
          filter:
            enabled:
            - name: QoSAwareNodeResourcesFit
          score:
            enabled:
            - name: QoSAwareNodeResourcesFit
              weight: 15
            - name: QoSAwareNodeResourcesBalancedAllocation
              weight: 1
            disabled:  
            - name: NodeResourcesMostAllocated
            - name: NodeResourcesBalancedAllocation
          reserve:
            enabled:
            - name: QoSAwareNodeResourcesFit
        pluginConfig:
        - name: NodeResourcesFit
          args:
            ignoredResourceGroups:
            - katalyst.kubewharf.io
        - name: QoSAwareNodeResourcesFit
          args:
            scoringStrategy:
              type: MostAllocated
              resources:
              - name: cpu
                weight: 1
              - name: memory
                weight: 1
              reclaimedResources:
              - name: "katalyst.kubewharf.io/reclaimed_millicpu"
                weight: 1
              - name: "katalyst.kubewharf.io/reclaimed_memory"
                weight: 1  
        - name: QoSAwareNodeResourcesBalancedAllocation # optional
          args:
            resources:
            - name: cpu
              weight: 1
            - name: memory
              weight: 1
            reclaimedResources:
            - name: "katalyst.kubewharf.io/reclaimed_millicpu"
              weight: 1
            - name: "katalyst.kubewharf.io/reclaimed_memory"
              weight: 1  
      ```

    - **Custom Strategy**

      ```yaml
      apiVersion: kubescheduler.config.k8s.io/v1beta1
      kind: KubeSchedulerConfiguration
      leaderElection:
        leaderElect: true
        resourceLock: leases
        resourceName: katalyst-scheduler
        resourceNamespace: katalyst-system
      profiles:
      - schedulerName: katalyst-scheduler
        plugins:
          preFilter:
            enabled:
            - name: QoSAwareNodeResourcesFit
          filter:
            enabled:
            - name: QoSAwareNodeResourcesFit
          score:
            enabled:
            - name: QoSAwareNodeResourcesFit
              weight: 15
            - name: QoSAwareNodeResourcesBalancedAllocation
              weight: 1
            disabled:  
            - name: RequestedToCapacityRatio
            - name: NodeResourcesBalancedAllocation
          reserve:
            enabled:
            - name: QoSAwareNodeResourcesFit
        pluginConfig:
        - name: NodeResourcesFit
          args:
            ignoredResourceGroups:
            - katalyst.kubewharf.io
        - name: QoSAwareNodeResourcesFit
          args:
            scoringStrategy:
              type: RequestedToCapacityRatio
              requestedToCapacityRatio:
                shape:
                - utilization: 0
                  score: 0
                - utilization: 100
                  score: 10
              reclaimedRequestedToCapacityRatio:
                shape:
                - utilization: 0
                  score: 0
                - utilization: 100
                  score: 10
              resources:
              - name: cpu
                weight: 1
              - name: memory
                weight: 1
              reclaimedResources:
              - name: "katalyst.kubewharf.io/reclaimed_millicpu"
                weight: 1
              - name: "katalyst.kubewharf.io/reclaimed_memory"
                weight: 1  
        - name: QoSAwareNodeResourcesBalancedAllocation # optional
          args:
            resources:
            - name: cpu
              weight: 1
            - name: memory
              weight: 1
            reclaimedResources:
            - name: "katalyst.kubewharf.io/reclaimed_millicpu"
              weight: 1
            - name: "katalyst.kubewharf.io/reclaimed_memory"
              weight: 1  
      ```

## Alternatives

- **Use `*kubernetes.io` as the resource key prefix for reclaimed resources**

    <table>
    <tr>
        <td></td>
        <td><code><b>katalyst.kubewharf.io</b></code></td>
        <td><code><b>*kubernetes.io</b></code></td>
    </tr>
    <tr>
        <td><b>Is Pod-level over-commitment supported</b><br>(requests != limits)</td>
        <td>No</td>
        <td>Yes</td>
    </tr>
    <tr>
        <td><b>Supported CPU units</b></td>
        <td>Support milli-core only
            <ul>
                <li>Because decimals are not allowed by K8s.</li>
            </ul>
        </td>
        <td>Support milli-core only
            <ul>
                <li>Although decimals are allowed by K8s, the values may be not correct.<br>When the <code>GetResourceRequest</code> method is called to get the amount of resources requested by a Pod, the <code>Value</code> method will round up the value.</li>
            </ul>
        </td>
    </tr>
    <tr>
        <td><b>The cost to modify the scheduler</b></td>
        <td>Low
            <ul>
                <li>We just need to do some configuration.<br>To be specific, we can use the ignoredResourceGroups feature of the native NodeResourcesFit plugin.</li>
            </ul>
        </td>
        <td>High
            <ul>
                <li>We need to disable the native NodeResourcesFit plugin and rewrite its logic in an extended plugin.<br>Because the ignoredResourceGroups feature does not take effect on resource types in the default namespace, that is, resource types prefixed with <code>*kubernetes.io</code> are not supported.</li>
            </ul>
        </td>
    </tr>
    <tr>
        <td><b>The cost to modify kubelet</b></td>
        <td>No
            <ul>
                <li>Kubelet ignores extended resources that are missing from the node's allocatable resources when admitting a Pod.</li>
            </ul>
        </td>
        <td>Extremely high
            <ul>
                <li>We need to hack kubelet's code.<br>Because for resource types prefixed with <code>*kubernetes.io</code>, kubelet doesn't ignore them when admitting a Pod.</li>
            </ul>
        </td>
    </tr>
    </table>

Considering that currently in the Katalyst system, the demand for Pod-level over-commitment of offline Pods is not strong. Also, by using `katalyst.kubewharf.io`, the cost to modify the scheduler and kubelet is much lower. **Therefore, we chose `katalyst.kubewharf.io`.**

## References

- [Resources outside the `*kubernetes.io` namespace are integers and cannot be over-committed](https://github.com/kubernetes/kubernetes/pull/48922)

- [kube-scheduler Configuration (v1beta3)](https://kubernetes.io/docs/reference/config-api/kube-scheduler-config.v1beta3/)

- [kube-scheduler Configuration (v1beta1)](https://v1-22.docs.kubernetes.io/docs/reference/config-api/kube-scheduler-config.v1beta1/)
