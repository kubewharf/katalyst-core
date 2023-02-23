# Concepts - katalyst core concepts
Katalyst contains a lot of components, making it difficult to dive deep. This documentation will introduce the basic concepts of katalyst to help developers understand how the system works, how it abstracts the QoS model, and how you can dynamically configure the system.

## Architecture
As shown in the architecture below, katalyst mainly contains three layers. For user-side API, katalyst defines a suit of QoS model along with multiple enhancements to match up with QoS requirements for different kinds of workload. Users can deploy their workload with different QoS requirements, and katalyst daemon will try to allocate proper resources and devices for those pods to satisfy their QoS requirements. This allocation process will work both at pod admission phase and runtime, taking into consideration the resource usage and QoS class of pods running on the same node. Besides, centralized components will cooperate with daemons to provide better resource adjustments for each workload with a cluster-level perspective.
<div align="center">
  <picture>
    <img src="./imgs/katalyst-overview.jpg" width=80% title="Katalyst Overview" loading="eager" />
  </picture>
</div>

## Components
Katalyst contains centralized components that are deployed as deployments, and agents that run as deamonsets on each and every node.

### Centralized Components

#### Katalyst Controllers/Webhooks

Katalyst controllers provide cluster-level abilities, including service profiling, elastic resource recommendation, core Custom Resource lifecycle management, and centralized eviction strategies run as a backstop. Katalyst webhooks are responsible for validating QoS configurations, and mutating resource requests according to service profiling.

#### Katalyst Scheduler

Katalyst scheduler is developed based on the scheduler v2 framework to provide the scheduling functionality for hybrid deployment and topology-aware scheduling scenarios

#### Custom Metrics API

Custom metrics API implements the standard custom-metrics-apiserver interface, and is responsible for collecting, storing, and inquiring metrics. It is mainly used by elastic resource recommendation and re-scheduling in the katalyst system.

### Daemon Components

#### QoS Resource Manager

QoS Resource Manager (QRM for short) is designed as an extended framework in kubelet, and it works as a new hint provider similar to Device Manager. But unlike Device Manager, QRM aims at allocating nondiscrete resources (i.e. cpu/memory) rather than discrete devices, and it can adjust allocation results dynamically and periodically based on container running status. QRM is implemented in kubewahrf enhanced kubernetes, and if you want to get more information about QRM, please refer to [qos-resource-manager](./proposals/qos-management/qos-resource-manager/20221018-qos-resource-manager.md).

#### Katalyst agent

Katalyst Agent is designed as the core daemon component to implement resource management according to QoS requirements and container running status. Katalyst agent contains several individual modules that are responsible for different functionalities. These modules can either be deployed as a monolithic container or separate ones.
- Eviction Manager is a framework for eviction strategies. Users can implement their own eviction plugins to handle contention for each resource type. For more information about eviction manager, please refer to [eviction-manager](./proposals/qos-management/eviction-manager/20220424-eviction-manager.md).
- Resource Reporter is a framework for different CRDs or different fields in the same CRD. For instance, different fields in CNR may be collected through different sources, and this framework makes it possible for users to implement each resource reporter with a plugin. For more information about reporter manager, please refer to [reporter-manager](./proposals/qos-management/reporter-manager/20220515-reporter-manager.md).
- SysAdvisor is the core node-level resource recommendation module, and it uses statistical-based, indicator-based, and ml-based algorithms for different scenarios. For more information about sysadvisor, please refer to [sys-advisor](proposals/qos-management/wip-20220615-sys-advisor.md).
- QRM Plugin works as a plugin for each resource with static or dynamic policies. Generally, QRM Plugins receive resource recommendations from SysAdvisor, and export controlling configs through CRI interface embedded in QRM Framework.

#### Malachite

Malachite is a unified metrics-collecting component. It is implemented out-of-tree, and serves node, numa, pod and container level metrics through an http endpoint from which katalyst will query real-time metrics data. In a real-world production environment, you can replace malachite with your own metric implementations.

## QoS

To extend the ability of kubernetes' original QoS level, katalyst defines its own QoS level with CPU as the dominant resource. Other than memory, CPU is considered as a divisible resource and is easier to isolate. And for cloudnative workloads, CPU is usually the dominant resource that causes performance problems. So katalyst uses CPU to name different QoS classes, and other resources are implicitly accompanied by it.

### Definition
<br>
<table>
  <tbody>
    <tr>
      <th align="center">Qos level</th>
      <th align="center">Feature</th>
      <th align="center">Target Workload</th>
      <th align="center">Mapped k8s QoS</th>
    </tr>
    <tr>
      <td>dedicated_cores</td>
      <td>
        <ul>
          <li>Bind with a quantity of dedicated cpu cores</li>
          <li>Without sharing with any other pod</li>
        </ul>
      </td>
      <td>
        <ul>
          <li>Workload that's very sensitive to latency</li>
          <li>such as online advertising, recommendation.</li>
        </ul>
      </td>
      <td>Guaranteed</td>
    </tr>
    <tr>
      <td>shared_cores</td>
      <td>
        <ul>
          <li>Share a set of dedicated cpu cores with other shared_cores pods</li>
        </ul>
      </td>
      <td>
        <ul>
          <li>Workload that can tolerate a little cpu throttle or neighbor spikes</li>
          <li>such as microservices for webservice.</li>
        </ul>
      </td>
      <td>Guaranteed/Burstable</td>
    </tr>
    <tr>
      <td>reclaimed_cores</td>
      <td>
        <ul>
          <li>Over-committed resources that are squeezed from dedicated_cores or shared_cores</li>
          <li>Whenever dedicated_cores or shared_cores need to claim their resources back, reclaimed_cores will be suppressed or evicted</li>
        </ul>
      </td>
      <td>
        <ul>
          <li>Workload that mainly cares about throughput rather than latency</li>
          <li>such as batch bigdata, offline training.</li>
        </ul>
      </td>
      <td>BestEffort</td>
    </tr>
    <tr>
      <td>system_cores</td>
      <td>
        <ul>
          <li>Reserved for core system agents to ensure performance</li>
        </ul>
      </td>
      <td>
        <ul>
          <li>Core system agents.</li>
        </ul>
      </td>
      <td>Burstable</td>
    </tr>
  </tbody>
</table>

#### Pool

As introduced above, katalyst uses the term `pool` to indicate a combination of resources that a batch of pods share with each other. For instance, pods with shared_cores may share a shared pool, meaning that they share the same cpusets, memory limits and so on; in the meantime, if `cpuset_pool` enhancement is enabled, the single shared pool will be separated into several pools based on the configurations.

### Enhancement

Beside the core QoS level,  katalyst also provides a mechanism to enhance the ability of standard QoS levels. The enhancement works as a flexible extensibility, and may be added continuously.

<br>
<table>
  <tbody>
    <tr>
      <th align="center">Enhancement</th>
      <th align="center">Feature</th>
    </tr>
    <tr>
      <td>numa_binding</td>
      <td>
        <ul>
          <li>Indicates that the pod should be bound into a (or several) numa node(s) to gain further performance improvements</li>
          <li>Only supported by dedicated_cores</li>
        </ul>
      </td>
    </tr>
    <tr>
      <td>cpuset_pool</td>
      <td>
        <ul>
          <li>Allocate a separated cpuset in shared_cores pool to isolate scheduling domain for identical pods.</li>
          <li>Only supported by shared_cores</li>
        </ul>
      </td>
    </tr>
    <tr>
      <td>...</td>
      <td>
      </td>
    </tr>
  </tbody>
</table>

## Configurations

To make the configuration more flexible, katalyst designs a new mechanism to set configs on the run, and it works as a supplement for static configs defined via command-line flags. In katalyst, the implementation of this mechanism is called `KatalystCustomConfig` (`KCC` for short). It enables each daemon component to dynamically adjust its working status without restarting or re-deploying.
For more information about KCC, please refer to [dynamic-configuration](proposals/qos-management/wip-20220706-dynamic-configuration.md).
