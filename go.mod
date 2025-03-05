module github.com/kubewharf/katalyst-core

go 1.18

require (
	github.com/agiledragon/gomonkey/v2 v2.13.0
	github.com/alecthomas/units v0.0.0-20190924025748-f65c72e2690d
	github.com/bytedance/mockey v1.2.11
	github.com/cespare/xxhash v1.1.0
	github.com/cilium/ebpf v0.7.0
	github.com/containerd/cgroups v1.0.1
	github.com/containerd/nri v0.6.0
	github.com/evanphx/json-patch v5.6.0+incompatible
	github.com/fsnotify/fsnotify v1.5.4
	github.com/gogo/protobuf v1.3.2
	github.com/golang/mock v1.6.0
	github.com/golang/protobuf v1.5.3
	github.com/google/cadvisor v0.44.2
	github.com/google/uuid v1.3.0
	github.com/h2non/gock v1.2.0
	github.com/klauspost/cpuid/v2 v2.2.6
	github.com/kubewharf/katalyst-api v0.5.6-0.20250723073136-24e693f5681c
	github.com/moby/sys/mountinfo v0.6.2
	github.com/montanaflynn/stats v0.7.1
	github.com/opencontainers/runc v1.1.6
	github.com/opencontainers/selinux v1.10.0
	github.com/pkg/errors v0.9.1
	github.com/prometheus/client_golang v1.14.0
	github.com/prometheus/client_model v0.3.0
	github.com/prometheus/common v0.37.0
	github.com/prometheus/procfs v0.10.0
	github.com/robfig/cron/v3 v3.0.1
	github.com/samber/lo v1.39.0
	github.com/slok/kubewebhook v0.11.0
	github.com/smartystreets/goconvey v1.6.4
	github.com/spf13/cobra v1.6.0
	github.com/spf13/pflag v1.0.5
	github.com/stretchr/testify v1.8.3
	github.com/vishvananda/netns v0.0.0-20200728191858-db3c7e526aae
	go.opentelemetry.io/otel v0.20.0
	go.opentelemetry.io/otel/exporters/metric/prometheus v0.20.0
	go.opentelemetry.io/otel/metric v0.20.0
	go.opentelemetry.io/otel/sdk v0.20.0
	go.opentelemetry.io/otel/sdk/export/metric v0.20.0
	go.opentelemetry.io/otel/sdk/metric v0.20.0
	go.uber.org/atomic v1.9.0
	golang.org/x/sys v0.13.0
	golang.org/x/time v0.0.0-20220210224613-90d013bbcef8
	gonum.org/v1/gonum v0.8.2
	google.golang.org/grpc v1.57.1
	gopkg.in/yaml.v3 v3.0.1
	gotest.tools/v3 v3.0.3
	k8s.io/api v0.26.1
	k8s.io/apimachinery v0.26.1
	k8s.io/apiserver v0.24.16
	k8s.io/autoscaler/cluster-autoscaler v0.0.0-20231010095923-8a61add71154
	k8s.io/autoscaler/vertical-pod-autoscaler v1.0.0
	k8s.io/client-go v0.26.1
	k8s.io/component-base v0.25.0
	k8s.io/component-helpers v0.24.16
	k8s.io/cri-api v0.25.3
	k8s.io/klog/v2 v2.80.1
	k8s.io/kube-aggregator v0.24.6
	k8s.io/kubelet v0.24.6
	k8s.io/kubernetes v1.24.16
	k8s.io/metrics v0.25.0
	k8s.io/utils v0.0.0-20221108210102-8e77b1f39fe2
	sigs.k8s.io/controller-runtime v0.11.2
	sigs.k8s.io/custom-metrics-apiserver v1.24.0
	sigs.k8s.io/descheduler v0.24.0
)

require (
	github.com/Azure/go-ansiterm v0.0.0-20210617225240-d185dfc1b5a1 // indirect
	github.com/Microsoft/go-winio v0.4.17 // indirect
	github.com/NYTimes/gziphandler v1.1.1 // indirect
	github.com/OneOfOne/xxhash v1.2.5 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/blang/semver/v4 v4.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/checkpoint-restore/go-criu/v5 v5.3.0 // indirect
	github.com/containerd/console v1.0.3 // indirect
	github.com/containerd/ttrpc v1.2.3-0.20231030150553-baadfd8e7956 // indirect
	github.com/coreos/go-semver v0.3.0 // indirect
	github.com/coreos/go-systemd/v22 v22.3.2 // indirect
	github.com/cyphar/filepath-securejoin v0.2.3 // indirect
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/docker/distribution v2.8.1+incompatible // indirect
	github.com/docker/go-units v0.4.0 // indirect
	github.com/emicklei/go-restful v2.16.0+incompatible // indirect
	github.com/emicklei/go-restful-swagger12 v0.0.0-20201014110547-68ccff494617 // indirect
	github.com/felixge/httpsnoop v1.0.1 // indirect
	github.com/go-logr/logr v1.2.3 // indirect
	github.com/go-openapi/jsonpointer v0.19.5 // indirect
	github.com/go-openapi/jsonreference v0.20.0 // indirect
	github.com/go-openapi/swag v0.19.15 // indirect
	github.com/godbus/dbus/v5 v5.0.6 // indirect
	github.com/golang/groupcache v0.0.0-20210331224755-41bb18bfe9da // indirect
	github.com/google/gnostic v0.6.9 // indirect
	github.com/google/go-cmp v0.5.9 // indirect
	github.com/google/gofuzz v1.2.0 // indirect
	github.com/gopherjs/gopherjs v0.0.0-20200217142428-fce0ec30dd00 // indirect
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway v1.16.0 // indirect
	github.com/h2non/parth v0.0.0-20190131123155-b4df798d6542 // indirect
	github.com/imdario/mergo v0.3.12 // indirect
	github.com/inconshreveable/mousetrap v1.0.1 // indirect
	github.com/josharian/intern v1.0.0 // indirect
	github.com/jpillora/backoff v1.0.0 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/jtolds/gls v4.20.0+incompatible // indirect
	github.com/karrick/godirwalk v1.16.1 // indirect
	github.com/mailru/easyjson v0.7.7 // indirect
	github.com/matttproud/golang_protobuf_extensions v1.0.2 // indirect
	github.com/mistifyio/go-zfs v2.1.2-0.20190413222219-f784269be439+incompatible // indirect
	github.com/moby/spdystream v0.2.0 // indirect
	github.com/moby/term v0.0.0-20210619224110-3f7ff695adc6 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/mrunalp/fileutils v0.5.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/mwitkow/go-conntrack v0.0.0-20190716064945-2f068394615f // indirect
	github.com/opencontainers/go-digest v1.0.0 // indirect
	github.com/opencontainers/runtime-spec v1.0.3-0.20220825212826-86290f6a00fb // indirect
	github.com/opentracing/opentracing-go v1.2.0 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/seccomp/libseccomp-golang v0.9.2-0.20220502022130-f33da4d89646 // indirect
	github.com/sirupsen/logrus v1.9.0 // indirect
	github.com/smartystreets/assertions v1.1.0 // indirect
	github.com/stretchr/objx v0.5.0 // indirect
	github.com/syndtr/gocapability v0.0.0-20200815063812-42c35b437635 // indirect
	github.com/vishvananda/netlink v1.1.0 // indirect
	go.etcd.io/etcd/api/v3 v3.5.4 // indirect
	go.etcd.io/etcd/client/pkg/v3 v3.5.4 // indirect
	go.etcd.io/etcd/client/v3 v3.5.4 // indirect
	go.opentelemetry.io/contrib v0.20.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc v0.20.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp v0.20.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp v0.20.0 // indirect
	go.opentelemetry.io/otel/trace v0.20.0 // indirect
	go.opentelemetry.io/proto/otlp v0.7.0 // indirect
	go.uber.org/multierr v1.7.0 // indirect
	go.uber.org/zap v1.19.1 // indirect
	golang.org/x/arch v0.0.0-20201008161808-52c3e6f60cff // indirect
	golang.org/x/crypto v0.14.0 // indirect
	golang.org/x/exp v0.0.0-20220303212507-bbda1eaf7a17 // indirect
	golang.org/x/net v0.17.0 // indirect
	golang.org/x/oauth2 v0.7.0 // indirect
	golang.org/x/sync v0.2.0 // indirect
	golang.org/x/term v0.13.0 // indirect
	golang.org/x/text v0.13.0 // indirect
	gomodules.xyz/jsonpatch/v3 v3.0.1 // indirect
	gomodules.xyz/orderedmap v0.1.0 // indirect
	google.golang.org/appengine v1.6.7 // indirect
	google.golang.org/genproto v0.0.0-20230706204954-ccb25ca9f130 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20230629202037-9506855d4529 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20230731190214-cbb8c96f2d6d // indirect
	google.golang.org/protobuf v1.31.0 // indirect
	gopkg.in/check.v1 v1.0.0-20201130134442-10cb98267c6c // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.0.0 // indirect
	gopkg.in/yaml.v2 v2.4.0 // indirect
	k8s.io/apiextensions-apiserver v0.24.2 // indirect
	k8s.io/cloud-provider v0.24.16 // indirect
	k8s.io/csi-translation-lib v0.24.16 // indirect
	k8s.io/kube-openapi v0.0.0-20221012153701-172d655c2280 // indirect
	k8s.io/kube-scheduler v0.24.6 // indirect
	k8s.io/mount-utils v0.24.16 // indirect
	sigs.k8s.io/apiserver-network-proxy/konnectivity-client v0.0.37 // indirect
	sigs.k8s.io/json v0.0.0-20220713155537-f223a00ba0e2 // indirect
	sigs.k8s.io/structured-merge-diff/v4 v4.2.3 // indirect
	sigs.k8s.io/yaml v1.3.0 // indirect
)

replace (
	github.com/kubewharf/katalyst-api => github.com/JulyWindK/katalyst-api v1.0.5
	k8s.io/api => k8s.io/api v0.24.6
	k8s.io/apiextensions-apiserver => k8s.io/apiextensions-apiserver v0.24.6
	k8s.io/apimachinery => k8s.io/apimachinery v0.24.6
	k8s.io/apiserver => k8s.io/apiserver v0.24.6
	k8s.io/cli-runtime => k8s.io/cli-runtime v0.24.6
	k8s.io/client-go => k8s.io/client-go v0.24.6
	k8s.io/cloud-provider => k8s.io/cloud-provider v0.24.6
	k8s.io/cluster-bootstrap => k8s.io/cluster-bootstrap v0.24.6
	k8s.io/code-generator => k8s.io/code-generator v0.24.6
	k8s.io/component-base => k8s.io/component-base v0.24.6
	k8s.io/component-helpers => k8s.io/component-helpers v0.24.6
	k8s.io/controller-manager => k8s.io/controller-manager v0.24.6
	k8s.io/cri-api => k8s.io/cri-api v0.24.6
	k8s.io/csi-translation-lib => k8s.io/csi-translation-lib v0.24.6
	k8s.io/klog => k8s.io/klog v1.0.0
	k8s.io/kube-aggregator => k8s.io/kube-aggregator v0.24.6
	k8s.io/kube-controller-manager => k8s.io/kube-controller-manager v0.24.6
	k8s.io/kube-openapi => k8s.io/kube-openapi v0.0.0-20220328201542-3ee0da9b0b42
	k8s.io/kube-proxy => k8s.io/kube-proxy v0.24.6
	k8s.io/kube-scheduler => k8s.io/kube-scheduler v0.24.6
	k8s.io/kubectl => k8s.io/kubectl v0.24.6
	k8s.io/kubelet => github.com/kubewharf/kubelet v1.24.6-kubewharf.9
	k8s.io/kubernetes => k8s.io/kubernetes v1.24.6
	k8s.io/legacy-cloud-providers => k8s.io/legacy-cloud-providers v0.24.6
	k8s.io/metrics => k8s.io/metrics v0.24.6
	k8s.io/mount-utils => k8s.io/mount-utils v0.24.6
	k8s.io/node-api => k8s.io/node-api v0.24.6
	k8s.io/pod-security-admission => k8s.io/pod-security-admission v0.22.8
	k8s.io/sample-apiserver => k8s.io/sample-apiserver v0.24.6
	k8s.io/sample-cli-plugin => k8s.io/sample-cli-plugin v0.24.6
	k8s.io/sample-controller => k8s.io/sample-controller v0.24.6
	sigs.k8s.io/json => sigs.k8s.io/json v0.0.0-20211020170558-c049b76a60c6
)
