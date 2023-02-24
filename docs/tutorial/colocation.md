# Tutorial - katalyst colocation best-practices
This guide introduces best-practices for colocation in katalyst with an end-to-end example. Follow the steps below to take a glance at the integrated functionality, and then you can replace the sample yamls with your workload when applying the system in your own production environment.

## Prerequisite
Please make sure you have deployed all pre-dependent components before moving on to the next step.

- Install enhanced kubernetes based on [install-enhanced-k8s.md](../install-enhanced-k8s.md)
- Install components according to the instructions in [Charts](https://github.com/kubewharf/charts.git). To enable full functionality of colocation, the following components are required while others are optional.
    - Agent
    - Controller
    - Scheduler

## Functionalities
Before going to the next step, let's assume that we will use those settings and configurations as baseline:

- Total resources are set as 48 cores and 195924424Ki per nod;
- Reserved resources for pods with shared_cores are set as 4 cores and 5Gi, and it means that we'll always keep at least this amount of resources for those pods for bursting requirements.
  
Based on the assumption above, you can follow the steps to deep dive into the colocation workflow.

### Resource Reporting
After installing, resource reporting module will report reclaimed resources. Since there are no pods running, the reclaimed resource will be calculated as:
`reclaimed_resources = total_resources - reserve_resources`

When you refer to CNR, reclaimed resources will be as follows, and it means that pods with reclaimed_cores can be scheduled onto this node.

```
status:
    resourceAllocatable:
        katalyst.kubewharf.io/reclaimed_memory: "195257901056"
        katalyst.kubewharf.io/reclaimed_millicpu: 44k
    resourceCapacity:
        katalyst.kubewharf.io/reclaimed_memory: "195257901056"
        katalyst.kubewharf.io/reclaimed_millicpu: 44k
```

Submit several pods with shared_cores, and put pressure on those workloads to make reclaimed resources fluctuate along with the running state of workload.

```
$ kubectl create -f ./examples/shared-normal-pod.yaml
```

After successfully scheduled, the pod starts running with cpu-usage ~= 1cores and cpu-load ~=1, and the reclaimed resources will be changed according to the formula below. We skip memory here since it's more difficult to reproduce with accurate value than cpu, but the principle is familiar.
`reclaim cpu = allocatable - round(ceil(reserve + max(usage,load.1min,load.5min))`

```
status:
    resourceAllocatable:
        katalyst.kubewharf.io/reclaimed_millicpu: 42k
    resourceCapacity:
        katalyst.kubewharf.io/reclaimed_millicpu: 42k
```
   
You can then put pressure on those pods to simulate requested peaks with `stress`, and the cpu-load will rise to approximately 3 to make the reclaimed cpu shrink to 40k.

```
$ kubectl exec shared-normal-pod -it -- stress -c 2
```
```
status:
    resourceAllocatable:
        katalyst.kubewharf.io/reclaimed_millicpu: 40k
    resourceCapacity:
        katalyst.kubewharf.io/reclaimed_millicpu: 40k
```

### Scheduling Strategy
Katalyst provides several scheduling strategies to schedule pods with reclaimed_cores. You can alter the default scheduling config, and then create deployment with reclaimed_cores.

```
$ kubectl create -f ./examples/reclaimed-deployment.yaml
```

#### Spread
Spread is the default scheduling strategy, and it will try to spread pods among all suitable nodes, and it is usually used to balance workload contention in the cluster. Apply Spread policy with the command below, and pod will be scheduled onto each node evenly.

```
$ kubectl apply -f ./examples/scheduler-policy-spread.yaml
$ kubectl get po -owide
```
```
NAME                             READY   STATUS    RESTARTS   AGE     IP              NODE          NOMINATED NODE   READINESS GATES
reclaimed-pod-5f7f69d7b8-4lknl   1/1     Running   0          3m31s   192.168.1.169   node-1   <none>           <none>
reclaimed-pod-5f7f69d7b8-656bz   1/1     Running   0          3m31s   192.168.2.103   node-2   <none>           <none>
reclaimed-pod-5f7f69d7b8-89n46   1/1     Running   0          3m31s   192.168.0.129   node-3   <none>           <none>
reclaimed-pod-5f7f69d7b8-bcpbs   1/1     Running   0          3m31s   192.168.1.171   node-1   <none>           <none>
reclaimed-pod-5f7f69d7b8-bq22q   1/1     Running   0          3m31s   192.168.0.126   node-3   <none>           <none>
reclaimed-pod-5f7f69d7b8-jblgk   1/1     Running   0          3m31s   192.168.0.128   node-3   <none>           <none>
reclaimed-pod-5f7f69d7b8-kxqdl   1/1     Running   0          3m31s   192.168.0.127   node-3   <none>           <none>
reclaimed-pod-5f7f69d7b8-mdh2d   1/1     Running   0          3m31s   192.168.1.170   node-1   <none>           <none>
reclaimed-pod-5f7f69d7b8-p2q7s   1/1     Running   0          3m31s   192.168.2.104   node-2   <none>           <none>
reclaimed-pod-5f7f69d7b8-x7lqh   1/1     Running   0          3m31s   192.168.2.102   node-2   <none>           <none>
```

#### Binpack
Binpack tries to schedule pods into a single node, until the node is unsuitable to schedule more, and it is usually used to squeeze resource utilization to a limited bound to rise utilization. Apply Binpack policy with the command below, and pods will be scheduled onto one intensive node.

```
$ kubectl apply -f ./examples/scheduler-policy-binpack.yaml
$ kubectl get po -owide      
```
```
NAME                             READY   STATUS    RESTARTS   AGE   IP              NODE      NOMINATED NODE   READINESS GATES
reclaimed-pod-5f7f69d7b8-7mjbz   1/1     Running   0          36s   192.168.1.176   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-h8nmk   1/1     Running   0          36s   192.168.1.177   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-hfhqt   1/1     Running   0          36s   192.168.1.181   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-nhx4h   1/1     Running   0          36s   192.168.1.182   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-s8sx7   1/1     Running   0          36s   192.168.1.178   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-szn8z   1/1     Running   0          36s   192.168.1.180   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-vdm7c   1/1     Running   0          36s   192.168.0.133   node-3    <none>           <none>
reclaimed-pod-5f7f69d7b8-vrr8w   1/1     Running   0          36s   192.168.1.179   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-w9hv4   1/1     Running   0          36s   192.168.2.109   node-2    <none>           <none>
reclaimed-pod-5f7f69d7b8-z2wqv   1/1     Running   0          36s   192.168.4.200   node-4    <none>           <none>
```

#### Custom
Besides those in-tree policies, you can also use self-defined scoring functions to customize scheduling strategy. In the example below, we use self-defined RequestedToCapacityRatio scorer as scheduling policy, and it will work the same as Binpack policy.
```
$ kubectl apply -f ./examples/scheduler-policy-custom.yaml
$ kubectl get po -owide
```
```
NAME                             READY   STATUS    RESTARTS   AGE   IP              NODE      NOMINATED NODE   READINESS GATES
reclaimed-pod-5f7f69d7b8-547zk   1/1     Running   0          7s    192.168.1.191   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-6jzbs   1/1     Running   0          6s    192.168.1.193   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-6v7kr   1/1     Running   0          7s    192.168.2.111   node-2    <none>           <none>
reclaimed-pod-5f7f69d7b8-9vrb9   1/1     Running   0          6s    192.168.1.192   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-dnn7n   1/1     Running   0          7s    192.168.4.204   node-4   <none>           <none>
reclaimed-pod-5f7f69d7b8-jtgx9   1/1     Running   0          7s    192.168.1.189   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-kjrlv   1/1     Running   0          7s    192.168.0.139   node-3    <none>           <none>
reclaimed-pod-5f7f69d7b8-mr85t   1/1     Running   0          6s    192.168.1.194   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-q4dz5   1/1     Running   0          7s    192.168.1.188   node-1    <none>           <none>
reclaimed-pod-5f7f69d7b8-v28nv   1/1     Running   0          7s    192.168.1.190   node-1    <none>           <none>
```

### QoS Controlling
After successfully scheduled, katalyst falls into the main QoS-controlling loop to dynamically adjust resource allocations in each node. In the current version, we use cpuset to isolate scheduling domain for pods in each pool, and memory limit to restrict upper bound of memory usage.

Before going to the next step, remember to clear previous pods to construct a pure environment.

Apply a pod with shared_cores as the command below. After ramp-up period, the cpuset for shared-pool will be 6 cores in total (i.e. reserved for 4 cores to reply for bursting, plus 2 cores for regular requirements). And the left cores are considered suitable for reclaimed pods.

```
$ kubectl create -f ./examples/shared-normal-pod.yaml
```
```
root@node-1:~# ./examples/get_cpuset.sh shared-normal-pod
Tue Jan  3 16:18:31 CST 2023
11,22-23,35,46-47
```

Apply a pod with reclaimed_cores as the command below, and the cpuset for reclaimed-pool will be 40 cores.

```
kubectl create -f ./examples/reclaimed-normal-pod.yaml
```
```
root@node-1:~# ./examples/get_cpuset.sh reclaimed-normal-pod
Tue Jan  3 16:23:20 CST 2023
0-10,12-21,24-34,36-45
```

Put pressure on the previous pod with shared_cores to make its load rise to 3, and the cpuset for shared-pool will be 8 cores in total (i.e. reserved for 4 cores to reply for bursting, plus 4 cores for regular requirements). And cores for reclaimed pool will shrink to 48 relatively.

```
$ kubectl exec reclaimed-normal-pod -it -- stress -c 2
```
```
root@node-1:~# ./examples/get_cpuset.sh shared-normal-pod
Tue Jan  3 16:25:23 CST 2023
10-11,22-23,34-35,46-47
```
```
root@node-1:~# ./examples/get_cpuset.sh reclaimed-normal-pod
Tue Jan  3 16:28:32 CST 2023
0-9,12-21,24-33,36-45
```

### Pod Eviction
Eviction is usually used as a common back-and-force method in case that the QoS fails to be satisfied, and we should always make sure pods with higher priority (i.e. shared_cores) to meet its QoS by evicting pods with lower  priority (i.e. reclaimed_cores). Katalyst contains both agent and central evictions to meet different requirements.

Before going to the next step, remember to clear previous pods to construct a pure environment.

#### Agent Eviction
Currently, katalyst provides several in-tree agent eviction implementations.

##### Resource OverCommit
Since reclaimed resources are always fluctuating according to the running state of pods with shared_cores, it may reduce shrink to a critical point that even pods will reclaimed pods can not run properly. In this case, katalyst will evict them to rebalance them to other nodes, and the comparison formula is as follows:
`sum(requested_reclaimed_resource) > alloctable_reclaimed_resource * threshold`

Apply several pods (including shared_cores and reclaimed_cores), and put some pressure to reduce allocatable reclaimed resources until it is below the tolerance threshold. It will finally trigger to evict pod reclaimed-large-pod-2.

```
$ kubectl create -f ./examples/shared-large-pod.yaml ./examples/reclaimed-large-pod.yaml
```
```
$ kubectl exec shared-large-pod-2 -it -- stress -c 40
```
```
status:
    resourceAllocatable:
        katalyst.kubewharf.io/reclaimed_millicpu: 4k
    resourceCapacity:
        katalyst.kubewharf.io/reclaimed_millicpu: 4k
```
```
$ kubectl get event -A | grep evict
default     43s         Normal   EvictCreated     pod/reclaimed-large-pod-2   Successfully create eviction; reason: met threshold in scope: katalyst.kubewharf.io/reclaimed_millicpu from plugin: reclaimed-resource-pressure-eviction-plugin
default     8s          Normal   EvictSucceeded   pod/reclaimed-large-pod-2   Evicted pod has been deleted physically; reason: met threshold in scope: katalyst.kubewharf.io/reclaimed_millicpu from plugin: reclaimed-resource-pressure-eviction-plugin
```

The default threshold for reclaimed resources 5, you can change it dynamically with KCC.

```$ kubectl create -f ./examples/kcc-eviction-reclaimed-resource-config.yaml```

##### Memory
Memory eviction is implemented in two parts: numa-level eviction and system-level eviction. The former is used along with numa-binding enhancement, while the latter is used for more general cases. In this tutorial, we will mainly demonstrate the latter. For each level, katalyst will trigger memory eviciton based on memory usage and Kswapd active rate to avoid slow path for memory allocation in kernel.

Apply several pods (including shared_cores and reclaimed_cores).

```$ kubectl create -f ./examples/shared-large-pod.yaml ./examples/reclaimed-large-pod.yaml```

Apply KCC to alert the default free memory and Kswapd rate threshold.

```$ kubectl create -f ./examples/kcc-eviction-memory-system-config.yaml```

###### For Memory Usage
Exec into reclaimed-large-pod-2 and request enough memory. When memory free falls below the target, it will trigger eviction for pods with reclaimed cores, and it will choose the pod that uses the most memory.

```
$ kubectl exec -it reclaimed-large-pod-2 bash
$ stress --vm 1 --vm-bytes 175G --vm-hang 1000 --verbose
```
```
$ kubectl get event -A | grep evict
default     2m40s       Normal   EvictCreated     pod/reclaimed-large-pod-2   Successfully create eviction; reason: met threshold in scope: memory from plugin: memory-pressure-eviction-plugin
default     2m5s        Normal   EvictSucceeded   pod/reclaimed-large-pod-2   Evicted pod has been deleted physically; reason: met threshold in scope: memory from plugin: memory-pressure-eviction-plugin
```
```
taints:
- effect: NoSchedule
  key: node.katalyst.kubewharf.io/MemoryPressure
  timeAdded: "2023-01-09T06:32:08Z"
```

###### For Kswapd
Login into the working node and put some pressure on system memory. When Kswapd active rates exceed the target threshold (default = 1),  it will trigger eviction for pods both for reclaimed cores and shared cores, but reclaimed cores will be prior to shared cores.

```
$ stress --vm 1 --vm-bytes 180G --vm-hang 1000 --verbose
```
```
$ kubectl get event -A | grep evict
default           2m2s        Normal    EvictCreated              pod/reclaimed-large-pod-2          Successfully create eviction; reason: met threshold in scope: memory from plugin: memory-pressure-eviction-plugin
default           92s         Normal    EvictSucceeded            pod/reclaimed-large-pod-2          Evicted pod has been deleted physically; reason: met threshold in scope: memory from plugin: memory-pressure-eviction-plugin
```
```
taints:
- effect: NoSchedule
  key: node.katalyst.kubewharf.io/MemoryPressure
  timeAdded: "2023-01-09T06:32:08Z"
```

##### Load
For pods with shared_cores, if any pod creates too many threads, the scheduling-period in cfs may be split into small pieces, and makes throttle more frequent, thus impacts workload performance. To solve this, katalyst implements load eviction to detect load counts and trigger taint and eviction actions based on threshold, and the comparison formula is as follows. 
``` soft: load > resource_pool_cpu_amount ```
``` hard: load > resource_pool_cpu_amount * threshold ```

Apply several pods (including shared_cores and reclaimed_cores).

```
$ kubectl create -f ./examples/shared-large-pod.yaml ./examples/reclaimed-large-pod.yaml
```

put some pressure to reduce allocatable reclaimed resources until the load exceeds the soft bound. In this case, taint will be added in CNR to avoid scheduling new pods, but the existing pods will keep running.

```
$ kubectl exec shared-large-pod-2 -it -- stress -c 50
```
```
taints:
- effect: NoSchedule
  key: node.katalyst.kubewharf.io/CPUPressure
  timeAdded: "2023-01-05T05:26:51Z"
```

put more pressure to reduce allocatable reclaimed resources until the load exceeds the hard bound. In this case, katalyst will evict the pods that create the most amount of threads.

```
$ kubectl exec shared-large-pod-2 -it -- stress -c 100
```
```
$ kubectl get event -A | grep evict
67s         Normal   EvictCreated     pod/shared-large-pod-2      Successfully create eviction; reason: met threshold in scope: cpu.load.1min.container from plugin: cpu-pressure-eviction-plugin
68s         Normal   Killing          pod/shared-large-pod-2      Stopping container stress
32s         Normal   EvictSucceeded   pod/shared-large-pod-2      Evicted pod has been deleted physically; reason: met threshold in scope: cpu.load.1min.container from plugin: cpu-pressure-eviction-plugin
```

#### Centralized Eviction
In some cases, the agents may suffer from the single point problem, i.e. in a large cluster, the daemon may fail to work because of a lot of abnormal cases, and it may cause the pods running in this node out of control. So,  centralized eviction by katalyst will try to evict all reclaimed pods to relieve this problem.
By default, if the readiness state keeps failing for 10 minutes, katalyst will taint the CNR as unSchedubable to make sure no more pods with reclaimed_cores can be scheduled into this node. And if the readiness state keeps failing for 20 minutes, it will try to evict all pods with reclaimed_cores.

```
taints:
- effect: NoScheduleForReclaimedTasks
  key: node.kubernetes.io/unschedulable
```

## Further More
We will try to provide more tutorials in the future along with feature releases in the future.
