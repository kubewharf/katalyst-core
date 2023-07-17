# Installation - kubewharf enhanced kubernetes
This guide shows how to get a kubewharf enhanced kubernetes up and running.  Notice that the setup of the kubernetes cluster in this guide is for demonstration purposes only.

## Prerequisite
The following instructions are tested on veLinux on Volcengine with kubeadm, and should apply to other DEB-based linux systems.
- The set-up process requires internet access from the hosts. So if you are trying to deploy enhanced kubernetes on Volcengine ECS, you need to use products like NAT.
- Since we use kubeadm as a deployment tool, do a sanity check on the hosts you are using to deploy kubernetes as described here.
- To adopt kubewharf to its full extent, we'd recommend Linux kernel 4.19 or higher.
 
## Download binaries
Some components and tools are delivered as binary artifacts. The release versions for these binaries are referenced from kubernetes changelog. Notice that kubelet/kubeadm/kubectl are redistributed by kubewharf.

```bash
mkdir deploy
cd deploy

wget https://github.com/containerd/containerd/releases/download/v1.4.12/containerd-1.4.12-linux-amd64.tar.gz
wget https://github.com/opencontainers/runc/releases/download/v1.1.1/runc.amd64
wget https://github.com/kubernetes-sigs/cri-tools/releases/download/v1.25.0/crictl-v1.25.0-linux-amd64.tar.gz
wget https://github.com/kubewharf/enhanced-k8s/releases/download/v1.24.6-kubewharf.4/kubernetes-node-linux-amd64.tar.gz

cd -
```

## Set up linux host
Install packages and make configuration changes which are required by the setup process or by kubernetes itself.
```bash
apt-get update && \
apt-get install -y apt-transport-https ca-certificates curl && \
apt install socat ebtables conntrack -y

modprobe br_netfilter

cat <<EOF | tee /etc/modules-load.d/k8s.conf
br_netfilter
EOF

cat <<EOF | tee /etc/sysctl.d/k8s.conf
net.bridge.bridge-nf-call-ip6tables = 1
net.bridge.bridge-nf-call-iptables = 1
net.ipv4.ip_forward = 1
vm.swappiness=0
EOF

sysctl -p /etc/sysctl.d/k8s.conf

swapoff -a
```

## Set up container runtime
As a more favored setup, containerd is used as the container runtime. This is required for both master and node.
```bash
cd deploy

tar xvf containerd-1.4.12-linux-amd64.tar.gz
tar xvf crictl-v1.25.0-linux-amd64.tar.gz

cd bin && ls | xargs -I {} install -p -D -m 0755 {} /usr/bin && cd -
install -p -D -m 0755 runc.amd64 /usr/bin/runc
install -p -D -m 0755 crictl /usr/bin/

mkdir /etc/containerd
cat <<EOF | tee /etc/containerd/config.toml
version = 2
root = "/var/lib/containerd"
state = "/run/containerd"

[grpc]
  max_recv_message_size = 16777216
  max_send_message_size = 16777216

[metrics]
  address = ""
  grpc_histogram = false

[plugins."io.containerd.grpc.v1.cri"]
  stream_server_address = "127.0.0.1"
  sandbox_image = "kubewharf/pause:3.7"
  max_container_log_line_size = -1
  disable_cgroup = false
  [plugins."io.containerd.grpc.v1.cri".containerd]
    snapshotter = "overlayfs"
    default_runtime_name = "runc"
    [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc]
      runtime_type = "io.containerd.runc.v2"
      [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.runc.options]
        NoPivotRoot = false
        NoNewKeyring = false
        SystemdCgroup = false
  [plugins."io.containerd.grpc.v1.cri".cni]
    bin_dir = "/opt/cni/bin"
    conf_dir = "/etc/cni/net.d"
    max_conf_num = 1
  [plugins."io.containerd.grpc.v1.cri".registry]
    config_path = "/etc/containerd/certs.d"
    [plugins."io.containerd.grpc.v1.cri".registry.mirrors]
      [plugins."io.containerd.grpc.v1.cri".registry.mirrors."docker.io"]
        endpoint = ["registry-1.docker.io"]
EOF

cat <<EOF | tee /etc/systemd/system/containerd.service
# Copyright The containerd Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     <http://www.apache.org/licenses/LICENSE-2.0>
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

[Unit]
Description=containerd container runtime
Documentation=https://containerd.io
After=network.target local-fs.target

[Service]
ExecStartPre=-/sbin/modprobe overlay
ExecStart=/usr/bin/containerd

Type=notify
Delegate=yes
KillMode=process
Restart=always
RestartSec=5
# Having non-zero Limit*s causes performance problems due to accounting overhead
# in the kernel. We recommend using cgroups to do container-local accounting.
LimitNPROC=infinity
LimitCORE=infinity
LimitNOFILE=1048576
# Comment TasksMax if your systemd version does not supports it.
# Only systemd 226 and above support this version.
TasksMax=infinity
OOMScoreAdjust=-999

[Install]
WantedBy=multi-user.target
EOF

# Start containerd
systemctl enable containerd
systemctl start containerd

# Check containerd status
systemctl status containerd

# Configure crictl
cat <<EOF | tee /etc/crictl.yaml
---
runtime-endpoint: unix:///var/run/containerd/containerd.sock
image-endpoint: unix:///var/run/containerd/containerd.sock
timeout: 30
debug: false
EOF

# Sanity check for crictl
crictl ps
```

## Set up kubernetes components
Install kubeadm, kubelet and kubectl. This is required for both master and node.

```bash
tar xvf kubernetes-node-linux-amd64.tar.gz

cd kubernetes/node/bin
install -p -D -m 0755 kubeadm /usr/bin
install -p -D -m 0755 kubelet /usr/bin
install -p -D -m 0755 kubectl /usr/bin
cd -

cat <<EOF | tee /lib/systemd/system/kubelet.service
[Unit]
Description=kubelet: The Kubernetes Node Agent
Documentation=https://kubernetes.io/docs/
Wants=network-online.target
After=network-online.target

[Service]
ExecStart=/usr/bin/kubelet
Restart=always
StartLimitInterval=0
RestartSec=10

[Install]
WantedBy=multi-user.target
EOF

mkdir -p /usr/lib/systemd/system/kubelet.service.d
cat <<EOF | tee /usr/lib/systemd/system/kubelet.service.d/10-kubeadm.conf
# Note: This dropin only works with kubeadm and kubelet v1.11+
[Service]
Environment="KUBELET_KUBECONFIG_ARGS=--bootstrap-kubeconfig=/etc/kubernetes/bootstrap-kubelet.conf \\
                        --kubeconfig=/etc/kubernetes/kubelet.conf"
Environment="KUBELET_CONFIG_ARGS=--config=/var/lib/kubelet/config.yaml"
# This is a file that "kubeadm init" and "kubeadm join" generates at runtime, populating the KUBELET_KUBEADM_ARGS variable dynamically
EnvironmentFile=-/var/lib/kubelet/kubeadm-flags.env
# This is a file that the user can use for overrides of the kubelet args as a last resort. Preferably, the user should use
# the .NodeRegistration.KubeletExtraArgs object in the configuration files instead. KUBELET_EXTRA_ARGS should be sourced from this file.
EnvironmentFile=-/etc/sysconfig/kubelet
ExecStart=
ExecStart=/usr/bin/kubelet \\
        \$KUBELET_KUBECONFIG_ARGS \\
        \$KUBELET_CONFIG_ARGS \\
        \$KUBELET_KUBEADM_ARGS \\
        \$KUBELET_EXTRA_ARGS \\
        \$KUBELET_HOSTNAME
EOF
```

## Set up kubernetes master
Set up the kubernetes master. You can adapt some configurations to your needs, e.g. `podSubnet`,`serviceSubnet`,`clusterDNS` etc.  Take down the outputs of the last two commands as these will be used later when joining the nodes. Notice that this guide only covers the setup of a single master cluster.

```bash
mkdir -p /etc/kubernetes
export KUBEADM_TOKEN=`kubeadm token generate`
export APISERVER_ADDR=<your master ip address>

cat <<EOF | tee /etc/kubernetes/kubeadm-client.yaml
apiVersion: kubeadm.k8s.io/v1beta3
bootstrapTokens:
- groups:
  - system:bootstrappers:kubeadm:default-node-token
  token: $KUBEADM_TOKEN
  ttl: 24h0m0s
  usages:
  - signing
  - authentication
kind: InitConfiguration
localAPIEndpoint:
  advertiseAddress: $APISERVER_ADDR
  bindPort: 6443
nodeRegistration:
  criSocket: unix:///var/run/containerd/containerd.sock
  imagePullPolicy: IfNotPresent
  name: $APISERVER_ADDR
  taints: []
---
apiServer:
  timeoutForControlPlane: 4m0s
  extraArgs:
    enable-aggregator-routing: "true"
apiVersion: kubeadm.k8s.io/v1beta3
certificatesDir: /etc/kubernetes/pki
clusterName: kubernetes
controllerManager: {}
dns: {}
etcd:
  local:
    dataDir: /var/lib/etcd
imageRepository: kubewharf
kind: ClusterConfiguration
kubernetesVersion: v1.24.6-kubewharf.4
networking:
  dnsDomain: cluster.local
  serviceSubnet: 172.23.192.0/18
  podSubnet: 172.28.208.0/20
scheduler: {}
controlPlaneEndpoint: $APISERVER_ADDR:6443
---
apiVersion: kubelet.config.k8s.io/v1beta1
authentication:
  anonymous:
    enabled: false
  webhook:
    cacheTTL: 0s
    enabled: true
  x509:
    clientCAFile: /etc/kubernetes/pki/ca.crt
authorization:
  mode: Webhook
  webhook:
    cacheAuthorizedTTL: 0s
    cacheUnauthorizedTTL: 0s
cgroupDriver: cgroupfs
clusterDNS:
- 172.23.192.10
clusterDomain: cluster.local
cpuManagerReconcilePeriod: 0s
evictionPressureTransitionPeriod: 0s
featureGates:
  KubeletPodResources: true
  KubeletPodResourcesGetAllocatable: true
  QoSResourceManager: true
fileCheckFrequency: 0s
healthzBindAddress: 127.0.0.1
healthzPort: 10248
readOnlyPort: 10255
httpCheckFrequency: 0s
imageMinimumGCAge: 0s
kind: KubeletConfiguration
logging:
  flushFrequency: 0
  options:
    json:
      infoBufferSize: "0"
  verbosity: 0
memorySwap: {}
nodeStatusReportFrequency: 0s
nodeStatusUpdateFrequency: 0s
rotateCertificates: true
runtimeRequestTimeout: 0s
shutdownGracePeriod: 0s
shutdownGracePeriodCriticalPods: 0s
staticPodPath: /etc/kubernetes/manifests
streamingConnectionIdleTimeout: 0s
syncFrequency: 0s
volumeStatsAggPeriod: 0s
EOF

kubeadm init --config=/etc/kubernetes/kubeadm-client.yaml --upload-certs -v=5

mkdir -p $HOME/.kube
cp -i /etc/kubernetes/admin.conf $HOME/.kube/config
chown $(id -u):$(id -g) $HOME/.kube/config

# Set up canal CNI
curl https://raw.githubusercontent.com/projectcalico/calico/v3.24.5/manifests/canal.yaml -O
kubectl create -f canal.yaml

# Take down the output of the following two lines
openssl x509 -pubkey -in /etc/kubernetes/pki/ca.crt | openssl rsa -pubin -outform der 2>/dev/null | openssl dgst -sha256 -hex | sed 's/^.* //'
kubeadm token create
```

## Join kubernetes nodes
To join a node to cluster, you need to copy some of the outputs when setting up master:
- `CA_CERTS_HASH`: the output of `openssl x509 -pubkey -in /etc/kubernetes/pki/ca.crt | openssl rsa -pubin -outform der 2>/dev/null | openssl dgst -sha256 -hex | sed 's/^.* //'` on master
- `KUBEADM_TOKEN`: the output of `kubeadm token create` on master. If you forgot the token, you can try `kubeadm token list` on master to get the previously created token. If the token expires, you can create a new one.
```bash
mkdir -p /etc/kubernetes/manifests
export APISERVER_ADDR=<your apiserver ip address>
export KUBEADM_TOKEN=<token created with 'kubeadm token create'>
export CA_CERTS_HASH=<hash of ca.crt>
export NODE_NAME=<name of the registered node>

cat <<EOF | tee /etc/kubernetes/kubeadm-client.yaml
apiVersion: kubeadm.k8s.io/v1beta2
kind: JoinConfiguration
discovery:
  bootstrapToken:
    apiServerEndpoint: $APISERVER_ADDR:6443
    token: $KUBEADM_TOKEN
    caCertHashes:
    - sha256:$CA_CERTS_HASH
  timeout: 60s
  tlsBootstrapToken: $KUBEADM_TOKEN
nodeRegistration:
  name: $NODE_NAME
  criSocket: unix:///var/run/containerd/containerd.sock
  tains: []
EOF

# Join the node to cluster
kubeadm join --config=/etc/kubernetes/kubeadm-client.yaml -v=5
```

Up to this point, you should have a kubewharf enhanced kubernetes cluster up and running.
