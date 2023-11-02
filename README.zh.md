[English](./README.md) | 简体中文

## 简介

katalyst 致力于解决云原生场景下的资源不合理利用问题，为资源管理和成本优化提供解决方案：
- QoS-Based 资源模型抽象：提供与业务场景匹配的资源 QoS 模型选择；
- 资源弹性管理：提供灵活可扩展的 HPA/VPA 资源弹性策略；
- 微拓扑及异构设备的调度、摆放：资源整体微拓扑感知调度、摆放，以及动态调整能力；
- 精细化资源分配、隔离：根据业务服务画像提供资源的精细化分配、出让和隔离

Katalyst 分为三个主要 Project：
- [Katalyst-API](https://github.com/kubewharf/katalyst-api.git) ：Katalyst 相关核心 API，包括 CRD、Protocol、QoS 定义等；
- [Katalyst-Core](https://github.com/kubewharf/katalyst-core.git) ：Katalyst 主体管控逻辑；
- [Charts](https://github.com/kubewharf/charts.git) ：Kubewharf 相关 Projects 的部署 helm charts；


更为详细的设计将在后续补充。


<div align="center">
  <picture>
    <img src="docs/imgs/katalyst-overview.jpg" width=80% title="Katalyst Overview" loading="eager" />
  </picture>
</div>

## 前置依赖

Katalyst 基于 Kubewharf 增强版 Kubernetes 发行版进行开发， 请参考 [kubewharf-enhanced-kubernetes](./docs/install-enhanced-k8s.md) 完成安装。

## 部署

您可以参考 [Charts](https://github.com/kubewharf/charts.git) 来完成 katalyst 的部署。由于 kubewharf enhanced kubernetes 基于特定版本的上游 kubernetes 进行开发，并且保持了与上游 kubernetes 的 API 兼容性，如果您需要部署其他组件（e.g. operator），请注意其与对应 kubernetes 版本的 API 兼容性。

## 示例

Katalyst 提供了丰富的样例为您展示相关的使用；详细内容请参考 [tutorials](./docs/tutorial/colocation.md)。

## 社区

### 会议

当前我们有尝试性的双周会议来讨论 proposal，分享 milestone，做 Q&A 等等。会议相关信息：
- 双周四 19:30 UTC+8
- [会议链接（飞书）](vc.feishu.cn/j/414822034)
- [会议日程](https://bytedance.feishu.cn/docx/VXGfdiddUoemoQx54uacwSHonPd)

### 联系方式

如果您有任何疑问，欢迎提交 GitHub issues 或者 pull requests，或者通过以下方式联系我们
- [Email](./MAINTAINERS.md)
- [飞书](https://applink.feishu.cn/client/chat/chatter/add_by_link?link_token=d2eo5b1d-87e0-428e-8625-15326353bcd4)

  <img src="/docs/imgs/lark-qrcode.png" width="200">

- [Slack](https://kubewharf.slack.com/archives/C0522F1HRGW)

### 贡献

若您期望成为 Katalyst 的贡献者，请参考 [CONTRIBUTING](CONTRIBUTING.md) 文档。

## 协议

Katalyst 采用 Apache 2.0 协议，协议详情请参考 [LICENSE](LICENSE)，另外 Katalyst 中的某些实现依赖于 Kubernetes 代码，此部分版权归属于 Kubernetes Authors。
