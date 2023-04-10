---
title: Katalyst Reporter Manager
authors:
  - "luomingmeng"
reviewers:
  - "waynepeking348"
  - "csfldf"
  - "NickrenREN"
creation-date: 2022-05-15 
last-updated: 2023-02-22
status: implemented
---

# Katalyst Reporter Manager

## Table of Contents

<!-- toc -->

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

<!-- /toc -->

## Summary

Katalyst expands the node resource with a new CRD Custom Node Resource (kcnr for short). This CRD contains both topology aware resources and reclaimed resources, but different fields in CNR may be collected through different sources. For instance, reclaimed resources are collected from SysAdvisor according to container running status, while topology aware resources are collected by each pluggable plugin. If each plugin patches CNR by itself, there may exist many API conflicts, and the QPS for API update requests may rise uncontrollably.

To solve this, Reporter Manager implements a common framework. Users can implement each resource reporter with a pluggable plugin, and the manager will merge the reported resources into the corresponding fields, and update the CR through one API request.

## Motivation

### Goals
- Implement a common framework to merge different fields for node-level CR, and update through one API request.
- Make the resource reporting component pluggable.

### Non-Goals
- Replace native resource representations in nodes.
- Replace device manager or device plugins for scalar resources in nodes.
- Implement the general resource, metrics, or device reporting logic.

## Proposal

### User Stories

#### Story 1
SysAdvisor is the core node-level resource recommendation module in Katalyst, and it will predict the resource requirements for non-reclaimed pods in real-time. If allocatable resources are surplus after matching up with the resource requirements, Katalyst will try to schedule reclaimed pods into this node for better resource utilization. And thus those reclaimed resources should be reported in CNR.

Since the reporting source is in SysAdvisor, Reporter Manager provides a mechanism for SysAdvisor to register an out-of-tree resource reporting plugin. So the boundary is clear, SysAdvisor is responsible for calculation logics for reclaimed resources, while Reporter Manager is responsible for updating it in CNR.

#### Story 2
In the production environment, we may have a lot of resources or devices that are affiliated with micro topology. To make those micro topology info appreciable in scheduling process, katalyst should report them in CNR. But  those resources or devices are usually bounded by specific vendors. If each vendor updates CNR by itself, there may exist many API conflicts, and the QPS for API update requests may rise uncontrollably.

In this case, Reporter Manager works as a coordinator. Each vendor can implement its own reporter plugin, and register itself into Reporter Manager. The latter will merge all the reporting fields, fix the conflicts, and update CNT through one API request.

### Design Overview
<div align="center">
  <picture>
    <img src="/docs/imgs/reporter-manager-overview.jpg" width=80% title="Katalyst Overview" loading="eager" />
  </picture>
</div>

- Reporter is responsible for updating CR to APIServer. For each CRD, there should exist one corresponding reporter. It receives all reporting fields from the Reporter Plugin Manager, validates the legality, merges them into the CR, and finally updates by clientset.
- Reporter Plugin Manager is the core framework. It listens to the registration events from each Reporter Plugin, periodically poll each plugin to obtain reporting info, and dispatch those info to different Reporters.
- Reporter Plugin is implemented by each resource or device vendor, and it is registered into Reporter Plugin Manager before it starts. Whenever it is called by Reporter Plugin Manager, it should return the fresh resource or device info for return, along with enough information about which CRD and which field those info are mapped with.

### API
Reporter Plugin communicates with Reporter Plugin Manager with GPRC, and the protobuf is shown as below. Each Reporter Plugin can report resources for more than one CRD, so it should explicitly nominate the GVR and fields in the protobuf.
```
message Empty {
}

enum FieldType {
  Spec = 0;
  Status = 1;
  Metadata = 2;
}

message ReportContent {
  k8s.io.apimachinery.pkg.apis.meta.v1.GroupVersionKind groupVersionKind = 1;
  repeated ReportField field = 2;
}

message ReportField {
  FieldType fieldType = 1;
  string fieldName = 2;
  bytes value = 3;
}

message GetReportContentResponse {
  repeated ReportContent content = 1;
}

service ReporterPlugin {
  rpc GetReportContent(Empty) returns (GetReportContentResponse) {}

  rpc ListAndWatchReportContent(Empty) returns (stream GetReportContentResponse) {}
}
```

Reporter Plugin Manager will implement the interfaces below.
```
type AgentPluginHandler interface {
   GetHandlerType() string // "ReportePlugin"
   PluginHandler
}

type PluginHandler interface {
   // Validate returns an error if the information provided by
   // the potential plugin is erroneous (unsupported version, ...)
   ValidatePlugin(pluginName string, endpoint string, versions []string) error
   // RegisterPlugin is called so that the plugin can be register by any
   // plugin consumer
   // Error encountered here can still be Notified to the plugin.
   RegisterPlugin(pluginName, endpoint string, versions []string) error
   // DeRegister is called once the pluginwatcher observes that the socket has
   // been deleted.
   DeRegisterPlugin(pluginName string)
}
```


Currently, katalyst only supports updating resources for CNR and native Node object. If you want to add a new node-level CRD, and report resources using this mechanism, you should implement a new Reporter in katalyst-core with the following interface.
```
type Reporter interface {
  // Update receives ReportField list from report manager, the reporter implementation
  // should be responsible for assembling and updating the specific object
   Update(ctx context.Context, fields []*v1alpha1.ReportField) error
   // Run starts the syncing logic of reporter
   Run(ctx context.Context)
}
```

### Design Details
To simplify, Reporter Manager only supports FieldType at the top-level of each object, i.e. only Status, Spec, Metadata are supported. If the object has embedded struct in Spec, you should explicitly nominate them in `FieldName`

For the `FieldName`, we will introduce the details about how to fill up the protobuf for each type.

#### Map
Different Reporter Plugin can report to the same Map field with different keys, and Reporter Manager will merge them together. But if multiple plugins report to the same Map field with the same key, Reporter Manager will always override the former one with the latter one, and the priority is based on the registration orders to make sure the override results are always deterministic.
- Plugin A (the former)
```
Field: []*v1alpha1.ReportField{
   {
      FieldType: v1alpha1.FieldType_Status,
      FieldName: "ReclaimedResourceCapacity",
      Value:     []byte(`&{"cpu": "10"}`),
   },
}
```
- Plugin B (the latter)
```
Field: []*v1alpha1.ReportField{
   {
      FieldType: v1alpha1.FieldType_Status,
      FieldName: "ReclaimedResourceCapacity",
      Value:     []byte(`&{"memory": "24Gi"}`),
   },
}
```
- Result (json)
```
{
    "reclaimedResourceCapacity": {
        "cpu": "10",
        "memory": "24Gi"
    }
}
```

#### Slice and Array
Different Reporter Plugin can report to the same Slice or Array field, and Reporter Manager will merge them together.
- Plugin A
```
Field: []*v1alpha1.ReportField{
   {
      FieldType: v1alpha1.FieldType_Status,
      FieldName: "ResourceStatus",
      Value:     []byte(`[&{"numa": "numa-1", "available": {"device-1": "12"}}]`),
   },
}
```
- Plugin B
```
Field: []*v1alpha1.ReportField{
   {
      FieldType: v1alpha1.FieldType_Status,
      FieldName: "ResourceStatus",
      Value:     []byte(`[&{"numa": "numa-2", "available": {"device-2": "13"}}]`),
   },
}
```
- Result (json)
```
{
    "resourceStatus": [
        {
           &{"numa": "numa-1", "available": {"device-1": "12"}},
           &{"numa": "numa-2", "available": {"device-2": "13"}}
        }
    ]
}
```
#### Common Field
If different Reporter Plugins try to report to the same common field (int, string, ...), Reporter Manager will always override the former one with the latter one, and the priority is based on the registration orders to make sure the override results are always deterministic.
- Plugin A (the former)
```
Field: []*v1alpha1.ReportField{
   {
      FieldType: v1alpha1.FieldType_Status,
      FieldName: "TopologyStatus",
      Value:     []byte(`&{"sockets": [&{"socketID": 0}]}`),
   },
}
```
- Plugin B (the latter)
```
Field: []*v1alpha1.ReportField{
   {
      FieldType: v1alpha1.FieldType_Status,
      FieldName: "TopologyStatus",
      Value:     []byte(`&{"sockets": [&{"socketID": 1}]}`),
   },
}
```
- Result (json)
```
{
    "topologyStatus": &{
        "sockets": [&{"socketID": 1}]
    }
}
```
## Alternatives
- All CNR resources are collected and updated by a monolith component, but this may cause this monolith component too complicated to maintain. In the meantime, all logics must be implemented in-tree, which will it be inconvenient to extend if new devices are needed.