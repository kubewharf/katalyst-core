<h1 align="center">
  <p align="center">Katalyst-core</p>
</h1>

English | [简体中文](./README.zh.md)

## Overview

katalyst aims to provide a universal solution to help improve resource utilization and optimize the overall costs in the cloud. The main feature includes:
- QoS-Based Resource Model: provide pre-defined QoS Model along with multiple enhancements to match up with QoS requirements for different kinds of workload; 
- Elastic Resource Management: provide both horizontal & vertical scaling implementations, along with an extensible mechanism for out-of-tree algorithms;
- Topology-Awared Scheduling and Allocating: expend ability of native scheduler and kubelet to support topology-awared resource scheduling, making it easy to support heterogeneous devices;
- Fine-Grained Resource Isolation: provide real-time and fine-grained resource oversold, allocation and isolation strategies for each QoS with auto-tuned workload profiling

Katalyst contains three main projects:
- [Katalyst-API](https://github.com/kubewharf/katalyst-api.git): Katalyst core API, including CRD, Protocol, QoS Model and so on;
- [Katalyst-Core](https://github.com/kubewharf/katalyst-core.git): Katalyst core implementations;
- [Charts](https://github.com/kubewharf/charts.git): Helm charts for all projects in Kubewharf;

More Detailed Introduction will be presented in the future.

<div align="center">
  <picture>
    <img src="docs/katalyst-overview.jpg" width=80% title="Katalyst Overview" loading="eager" />
  </picture>
</div>

## Prerequisites

Katalyst runs on a Kubewharf enhanced kubernetes cluster. Please refer to [kubewharf-enhanced-kubernetes](./docs/install-enhanced-k8s.md) for detailed deployment instructions.

## Deploying

Please refer to [Charts](https://github.com/kubewharf/charts.git) for detailed helm charts. 
Since Kubewharf enhanced kubernetes is developed based on specific versions of upstream Kubernetes and maintains API compatibility with corresponding Kubernetes versions, if you wish to run other components (e.g. operators), please note its compatibility with the corresponding Kubernetes API version.

## Getting started

Katalyst provides several example yaml to demonstrate the common use cases. (todo)

## Community

### Contributing

If you are willing to be a contributor for the Katalyst project, please refer to our [CONTRIBUTING](CONTRIBUTING.md) document for details.

### Contact

If you have any questions or want to contribute, you are welcome to communicate most things via GitHub issues or pull requests.
Or Contact [Maintainers](./MAINTAINERS.md)

## License

Katalyst is under the Apache 2.0 license. See the [LICENSE](LICENSE) file for details.
Certain implementations in Katalyst rely on the existing code from Kubernetes and the credits go to the original Kubernetes authors.
