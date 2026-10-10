# kuberoot

[Русская версия](README.ru.md)

kuberoot builds Kubernetes distributions that boot straight on hardware. Each one is a single image with its own Linux kernel, a small init that brings up Kubernetes, and a node that is managed through the Kubernetes API itself: there is no SSH, no shell and no separate node tool, `kubectl` reaches the node. Everything above the base is a [kubepkg](https://github.com/kuberoot-dev/kubepkg) package, so a distribution is a base plus a meta package, the way a Linux distribution is a kernel plus packages.

The repository builds eight distributions: `edge`, a general-purpose cluster for one to a few nodes; `router`, Kubernetes without containers that runs a network router; `hypervisor`, virtual machines on KVM with their disks replicated between nodes, moved off a node that fails; `ai`, language models served as processes of the nodes behind one OpenAI-compatible endpoint, with new versions rolled out and back by the cluster; `rt`, a real-time controller whose pods get whole CPUs with no timer tick on a fully preemptible kernel; `iot`, a gateway with no containers that reads devices over Modbus and sends on what they say, its changes undone by themselves when they cut a device off; `observability`, a monitoring cluster outside what it watches, which reads servers' BMCs, switches, PDUs and probes of services natively and tells the outside world, by its heartbeats, that it still works; and `workstation`, people's desktops as virtual machines of the cluster, opened from a browser or an RDP client.

> Status: experimental. The `edge` distribution passes the Kubernetes conformance suite (462 of 462 on a three-node cluster, Kubernetes 1.37.1), but the API of the node and the layout of the state may still change.

## What a node is

- **Kernel**: Linux 6.18 built for the distribution, with every driver built in. The only modules a node loads are the ones its profile names (DRBD for replicated storage), signed with the build's key; after them, loading modules is switched off.
- **kinit**: PID 1. It mounts the read-only root and the state partition, brings up the network, issues the node's certificates and runs the services of the node's role: containerd, kubelet and kube-proxy everywhere, plus kine (the cluster store on SQLite), the API server, the controller manager and the scheduler on the control plane.
- **Node API**: every node serves `node.kuberoot.dev`, aggregated into the cluster API, so `kubectl get disks`, `kubectl get bootentries` or `kubectl create upgrade` act on a node. It works before a cluster exists, which is how a node is installed.
- **Pod network**: VXLAN between nodes, or plain routes on a flat network, or none, for a CNI package such as Cilium.
- **Packages**: the kubepkg operator runs on the control plane and installs the distribution's packages; the `edge` meta package and the platform packages live in [kubepkg-recipes](https://github.com/kuberoot-dev/kubepkg-recipes).

## The router distribution

`router` keeps only the part of Kubernetes that holds and watches configuration: kine, the API server, a controller manager for garbage collection, and the node API. There is no kubelet, container runtime, kube-proxy or scheduler. The router's configuration is resources of `router.kuberoot.dev`, with RBAC, audit, watch and GitOps as for any resource, and `kuberoot-router` makes the node match them:

| Resource | What it configures |
|---|---|
| `Interface` | a link's addresses and MTU, or a VLAN on a parent link |
| `Route` | a static route |
| `NATRule` | masquerade behind an outgoing link, or a port forwarded to a host |
| `FirewallZone`, `FirewallRule` | zones of links, what may reach the router and what may cross between zones |
| `DHCPServer` | DHCP on a link, and a DNS cache for it |
| `BGPRouter`, `BGPPeer` | the router's AS and announced networks, and its BGP sessions |
| `Safeguard` | changes on trial, undone unless confirmed in time |

A home gateway, with a LAN on `eth1` behind the uplink `eth0`:

```yaml
apiVersion: router.kuberoot.dev/v1alpha1
kind: Interface
metadata: {name: lan}
spec: {link: eth1, addresses: [192.168.10.1/24]}
---
apiVersion: router.kuberoot.dev/v1alpha1
kind: NATRule
metadata: {name: out}
spec: {masquerade: {outLink: eth0, sources: [192.168.10.0/24]}}
---
apiVersion: router.kuberoot.dev/v1alpha1
kind: DHCPServer
metadata: {name: lan}
spec: {link: eth1, rangeStart: 192.168.10.100, rangeEnd: 192.168.10.199}
---
apiVersion: router.kuberoot.dev/v1alpha1
kind: FirewallZone
metadata: {name: wan}
spec: {links: [eth0], input: Drop, management: true}
---
apiVersion: router.kuberoot.dev/v1alpha1
kind: FirewallZone
metadata: {name: lan}
spec: {links: [eth1], input: Accept, forwardTo: [wan]}
```

Every resource reports in its status whether the node runs it and, if not, why; interfaces report their state, DHCP servers their leases, BGP peers their sessions. The firewall filters only once a zone exists, and the router's API stays open on management zones whatever the rules say. With a `Safeguard`, every change runs on trial: unless `spec.confirm` names its revision in time, the router goes back to the last confirmed configuration, so a change that cuts the administrators off undoes itself.

## The hypervisor distribution

`hypervisor` runs virtual machines on KVM with cloud-hypervisor: no libvirt, and no pods. A machine is a resource of the cluster:

```yaml
apiVersion: vm.kuberoot.dev/v1alpha1
kind: VirtualMachine
metadata: {name: web}
spec:
  cpus: 2
  memory: 2Gi
  replicas: 3
  disk: {size: 20Gi, image: "https://example.org/images/debian-13-uefi.qcow2"}
```

`kuberoot-vmctl`, on the control plane, places the machine's disk on as many nodes as it has replicas and the machine on one of them. The disk is a DRBD volume: every write reaches the other nodes before it is done, and a node cut off from the majority stops writing. When the machine's node has been down for half a minute, the machine starts on another node holding its disk, with the same disk and address; the node that comes back catches up and drops its copy of the machine. Machines share one network across the nodes (a bridge on each, joined by VXLAN), with a gateway on the control plane for DHCP and the way out.

A running machine moves to another node alive when its `spec.node` changes to another of its replica nodes: the disk is writable on both for the moment of the move, the machine's memory is copied over while it runs, and it goes on there with the same disk and address, without a reboot. A move that does not finish in ten minutes, or loses a node on the way, is called off and the machine stays where it was; `status.failedMigration` names the node until `spec.node` changes again.

Each node serves its part through the node API: `volumes` (the disks on it, with their DRBD role and state), `machines` (the machines it runs) and `machines/console` (a machine's serial console). The kubelet stays for what the cluster knows of its nodes, their registration and heartbeat; no pods run.

## The ai distribution

`ai` is a cluster for inference and training: edge, pods and packages included, with a way of its own to serve language models that needs no pods and no container images. Each node runs llama-server, built into the image, as a process of its own. A model is a resource of the cluster:

```yaml
apiVersion: ai.kuberoot.dev/v1alpha1
kind: Model
metadata: {name: qwen}
spec:
  replicas: 3
  contextSize: 4096
  parallel: 2
  source:
    url: https://example.org/models/qwen2.5-0.5b-instruct-q4_k_m.gguf
    sha256: 74a4da8c9fdbcd15bd1f6d01d621410d31c6fc00986f5eb687824e7b93d7a9db
```

`kuberoot-aictl`, on the control plane, places the model on as many nodes as it has replicas. Each node downloads the weights, uses them only if they match their hash, and starts the server. The control plane serves every model at one OpenAI-compatible endpoint, port 8000: `/v1/models`, and `/v1/chat/completions` and the other `/v1` calls, routed by the model the request names to its ready replicas in turn, with streamed answers passed through as they come. The endpoint has no authentication of its own yet: it is meant for a network the cluster trusts.

A new `spec.source` is a new version. It goes to one node first; once it is ready there and answers a trial request, the other nodes take it one at a time, each after the one before serves it, so the model never loses more than one replica to the change. A version that fails to start on the first node, is not ready there in fifteen minutes, or does not answer, is rolled back: every node stays on the version before, and `status.failedVersion` keeps it from being tried again until `spec.source` changes. `status.previous` is the version before the current one; its weights stay on the nodes, and setting `spec.source` back to it rolls the model back at once.

Each node serves its part through the node API: `modelservers` (the models it serves, their phase and the weights they run) and `modelservers/log` (the server's output).

### GPUs and packages

The ai distribution is edge with pods and packages, so everything else for AI work comes as kubepkg packages: GPU sharing, queues, serving engines, training and vector databases, and ready-made combinations of them.

The NVIDIA driver is part of the image, not a container that installs it. The open kernel modules are built against kuberoot's kernel and signed with its key, the only modules it loads. The CUDA driver library, NVML, nvidia-smi and the GSP firmware come from the same driver release. A node with NVIDIA GPUs loads the driver at boot. kuberoot-node creates the device files and describes the GPUs to containerd as CDI devices (`nvidia.com/gpu=0`, `=all`), for a device plugin to hand them to pods, and OSConfig lists them in `status.gpus`; kuberoot-aictl labels such nodes `nvidia.com/gpu.present=true` for GPU packages to find them, and the NVIDIA CDI hook in the image refreshes a container's loader cache for the driver libraries it is given. A node without NVIDIA GPUs goes on without the driver. A Model can run on GPUs too: `spec.gpus` per replica places it only on nodes with as many free and runs it with every layer on the GPUs, served by the engine its source names: a llama-server built for CUDA (Turing to Hopper), published beside each release. An engine is fetched like the weights, by the nodes that run it, so the image stays small and the engine changes without the OS; a new engine is a new version of the model and rolls out like new weights. GPUs given to models are best kept from the device plugin's pods.

### The operator agent

`kuberoot-agent` watches the cluster and asks a language model what to do about what it finds: nodes not ready, services of nodes restarting, model servers that fail, models short of replicas. The model is one the cluster serves, or any OpenAI-compatible endpoint. It never acts. It proposes a `Remedy`, one of five actions: restart a node's service, restart a model's server on a node, roll a model back to the version before, reboot a node, or escalate (change nothing and leave it to a person). The model picks only the action, held to the ones open for the problem by a JSON schema. What the action applies to comes from the problem, not from the model.

```yaml
apiVersion: ai.kuberoot.dev/v1alpha1
kind: Agent
metadata: {name: default}
spec:
  model: qwen
  autoApprove: []      # actions that run without a person, e.g. [RestartModelServer]
  maxPerHour: 4
```

A remedy runs once a person approves it (`kubectl patch remedy <name> --type merge -p '{"spec":{"approved":true}}'`), or at once if the Agent's `autoApprove` lists its action. `kuberoot-aictl` runs it, one a pass and no more than `maxPerHour`. It refuses, whoever approved it, to reboot the control plane or to reboot a node while another is down. The agent has an identity of its own: its role reads the cluster and creates remedies, and an admission policy keeps it from approving, changing or removing any. Remedies, with the agent's reason and the evidence it was shown, are the record of what it did and wanted to do.
## The rt distribution

`rt` is a real-time controller: a control loop runs in a pod and wakes on time. The kernel is fully preemptible (PREEMPT_RT). CPUs 0 and 1 keep the node's own work, its interrupts and RCU callbacks; from CPU 2 on, CPUs run with no timer tick while one task runs on them. The kubelet hands whole CPUs out of those to Guaranteed pods that ask for whole CPUs, with their memory from the NUMA node of their CPUs, so the loop has its CPUs to itself:

```yaml
apiVersion: v1
kind: Pod
metadata: {name: loop}
spec:
  containers:
  - name: loop
    image: registry.example.org/plant/loop:1.4
    command: [chrt, --fifo, "80", /loop]
    securityContext:
      capabilities: {add: [SYS_NICE]}
    resources:
      requests: {cpu: "1", memory: 256Mi}
      limits: {cpu: "1", memory: 256Mi}
```

A task with CAP_SYS_NICE runs at a real-time priority (SCHED_FIFO); real-time tasks may take a CPU whole, and the kernel's fair server keeps the node's own work running beside them. SCHED_DEADLINE is not offered to pods: the kernel admits it only for tasks free to run on all the CPUs of their scheduling domain, and a pod's are pinned.

How late the node wakes a real-time task is a resource of the node API, `latencytests`: cyclictest runs a SCHED_FIFO thread on each CPU asked for, the CPUs without a timer tick by default, and the test reports per CPU and overall the least, mean and most latency and the 99th and 99.99th percentiles.

```yaml
apiVersion: node.kuberoot.dev/v1alpha1
kind: LatencyTest
metadata: {name: baseline}
spec: {durationSeconds: 300}
```

The distribution needs four CPUs or more. The kernel is built for it apart from the other distributions' (`distros/rt/kernel.config`); on amd64 its CPU layout is part of the kernel's own command line.

## The iot distribution

`iot` is a gateway: it reads devices on the plant floor and sends on what they say. Like the router, it is Kubernetes without containers. The node keeps its devices and routes in its own API server as `devices.kuberoot.dev` resources, and `kuberoot-devices`, a process of the node, does the work. There is no kubelet, container runtime, kube-proxy or scheduler. The node is its kernel, its init, the API server and one controller.

```yaml
apiVersion: devices.kuberoot.dev/v1alpha1
kind: Device
metadata: {name: press-7}
spec:
  protocol: modbus-tcp
  address: 10.0.5.20:502
  every: 1s
  points:
  - {name: temperature, kind: holding, register: 0, type: int16, scale: "0.1", unit: "C"}
  - {name: running, kind: coil, register: 0}
---
apiVersion: devices.kuberoot.dev/v1alpha1
kind: Route
metadata: {name: overheat}
spec:
  device: press-7
  points: [temperature]
  when: "running && temperature > 80"
  to:
    mqtt: {broker: "tcp://broker.plant:1883", topic: factory/press-7/alarm, qos: 1}
```

A Device is read on its schedule over one Modbus TCP connection. Its status shows whether it answers, and its values (`kubectl get devices` lists them). A Route sends every reading to an MQTT topic, or, with `when`, a CEL expression over the device's points, only each time the expression becomes true. A route that keeps its device and condition across a change remembers whether the condition held, so a change does not send the same alarm twice.

Changes run on trial under a Safeguard (`kind: Safeguard`, named `default`). A change that makes a device that was answering go silent, or a broker that was taking messages unreachable, is undone at once: the node goes back to the last confirmed configuration, keeps it across reboots, and marks the resources it does not run `RolledBack` with the reason. A change that runs its trial healthy becomes final by itself, or only by `spec.confirm` with `autoConfirm: false`.

Why without pods: a gateway does one job, close to the devices, often on small hardware and with nobody nearby. Here the protocol code, the configuration and the operating system are one image, upgraded together with A/B slots and rolled back together. The configuration is typed and checked when it is written. It goes through RBAC and audit, can be reviewed like code, and is undone by itself when it cuts a device off. On a test node the whole system took about 630 MiB, most of it the API server; `kuberoot-devices` took 35 MiB.

## The observability distribution

`observability` is a monitoring cluster that stands outside what it watches, so it still sees and alerts when that is down. It is edge, with pods and packages: VictoriaMetrics, VictoriaLogs, alerting and Perses dashboards come from the `kuberoot-observability` meta package. What it watches beyond software are `devices.kuberoot.dev` resources, read by `kuberoot-devices` on the control plane with no exporters to run:

```yaml
apiVersion: devices.kuberoot.dev/v1alpha1
kind: Device
metadata: {name: bmc-a3-01}
spec:
  protocol: redfish
  address: https://10.0.9.11
  every: 30s
  tls: {insecureSkipVerify: true}
  credentials: {name: bmc-rack-a3}     # a Secret in kube-system: username, password
---
apiVersion: devices.kuberoot.dev/v1alpha1
kind: Device
metadata: {name: pdu-a3}
spec:
  protocol: snmp
  address: 10.0.9.20
  snmp: {version: "3"}
  credentials: {name: snmp-noc}        # username, authProtocol, authPassword, privProtocol, privPassword
  points:
  - {name: load, oid: 1.3.6.1.4.1.318.1.1.12.2.3.1.1.2.1, scale: "0.1", unit: A}
---
apiVersion: devices.kuberoot.dev/v1alpha1
kind: Device
metadata: {name: portal}
spec: {protocol: http, address: "https://portal.example.org/healthz", every: 15s}
---
apiVersion: devices.kuberoot.dev/v1alpha1
kind: Heartbeat
metadata: {name: outside}
spec:
  url: https://hc.example.org/ping/0b6f...
  every: 1m
  query:
    url: http://vmsingle-victoria-metrics.monitoring.svc:8428
    expr: max(time() - timestamp(kuberoot_device_up)) < 120
```

A BMC reports what it has: the daemon walks its Redfish systems, chassis and managers and names the values after them, as `temperature_cpu1_temp_celsius`, `fan_..._rpm`, `power_..._watts`, `psu_..._health` (0 OK, 1 warning, 2 critical) and the entries of its logs. An SNMP device reads the OIDs its points name, over version 2c or 3. A probe (`http`, `tcp`, `icmp`) measures `up`, `latency_seconds` and the HTTP `status`. A service that is down is a reading too. Every value is a Prometheus metric, `kuberoot_device_value{device,point}` with `kuberoot_device_up`, on the node's port 9790, behind the Service `kube-system/kuberoot-devices` that the daemon keeps on the node's address. The meta package scrapes it, shows it in a Perses dashboard (dashboards and datasources are `PersesDashboard` and `PersesGlobalDatasource` resources), and alerts on devices that stop answering and on health that turns critical. Changes run on trial under a Safeguard, as on iot: one that breaks a target that worked is undone.

The heartbeat is the dead man's switch: it calls a URL outside the cluster every period, and only while its query finds what it looks for, so a cluster that stopped collecting falls as silent as one that is down, and the outside service alerts. The meta package's `Watchdog` alert, always firing, serves the same end for the alerting chain when Alertmanager routes it to such a service.

## The workstation distribution

`workstation` keeps people's desktops in the cluster: VDI without Citrix or vCenter. It is the hypervisor distribution with workspaces on top. A workspace is a virtual machine with a desktop and a replicated disk. It keeps running when the laptop that opened it closes, and it moves between nodes like any machine: on a test cluster a desktop session held open through the gateway kept answering, its slowest frame under 10 ms, while the desktop moved alive to another node.

```yaml
apiVersion: workstation.kuberoot.dev/v1alpha1
kind: Workspace
metadata: {name: anna}
spec:
  owner: anna
  cpus: 2
  memory: 2Gi
  disk: 6Gi
```

`kuberoot-wsctl`, on the control plane, makes the workspace a VirtualMachine from a cloud image (Ubuntu 24.04 unless `spec.image` names another). The image's cloud-init installs the desktop at the first boot: the owner's account, Xfce, a VNC server that keeps the session running between connections, and xrdp for RDP clients. The workspace is `Provisioning` until its desktop answers, then `Ready`. Its status gives the address to open it at in a browser (`status.url`) and the one for an RDP client (`status.rdp`). The Secret `status.credentials` names holds the workspace's token and the owner's password.

The gateway is part of `kuberoot-wsctl`, a process of the control plane node, port 6080. For a browser that brings the workspace's token it serves noVNC and carries noVNC's WebSocket to the desktop's VNC server, which then asks for the owner's password. Each workspace also gets an RDP port of its own on the node, passed through to xrdp. cloud-hypervisor has no graphical console, so the desktop is served by the guest itself; the cluster gives the way in. The gateway serves plain HTTP for now: put it behind TLS, and single sign-on (OIDC) in place of tokens is the next step.

The machine's cloud-init data comes from the machines' gateway, not from a seed disk: a VirtualMachine may carry `userData`, inline or in a Secret, which the gateway serves to that machine's address only, and the machine finds it through its DMI serial number (`ds=nocloud;s=…`). Any VirtualMachine of the hypervisor distribution can use it.

A Windows desktop fits the same way: a Windows image with RDP enabled and its own unattended setup in place of cloud-init, and RemoteApp to open single applications rather than a whole desktop. It needs licences, which is why the demonstration uses Linux.

## Several control planes

A single control plane keeps the cluster's state in kine on SQLite: no peers, nothing to tune. A cluster that should survive the loss of a control plane keeps it in etcd instead, chosen when the cluster is created: boot the first node with `kuberoot.datastore=etcd` (on the boot media, `mkimage -args kuberoot.datastore=etcd`). The choice stays with the cluster; a cluster created on kine stays on kine.

On etcd, the first control plane runs a member of the store, with an authority of its own for the store's peers and clients, apart from the cluster's, which also signs kubelets' certificates. More control planes join through the node API, like workers, with the role and the address the other members reach them at:

```yaml
apiVersion: node.kuberoot.dev/v1alpha1
kind: JoinTicket
metadata: {name: cp-2}
spec:
  nodeName: kuberoot-dcaf7a
  role: ControlPlane
  address: 10.0.0.12
```

The ticket adds the node to the store as a learner, which gets its vote once it has caught up with the others, and hands it what every control plane holds the same: the cluster's authorities, the service account key and the key Secrets are encrypted with. Applied as the node's `Membership`, it runs an API server, a controller manager and a scheduler of its own; controllers and the package manager elect a leader among control planes. Three control planes keep the cluster working when one is lost; five, when two are.

Workers that join a cluster of several control planes reach the API through a balancer on the node, in the node API: it forwards each connection to a control plane that answers and follows the control planes in the cluster's `kubernetes` Service as they come and go. No virtual address and no load balancer outside the cluster are needed, so it works on any network.

`etcdmembers`, on a control plane, lists the store's members; deleting one removes it, as replacing a lost control plane needs: remove its member, then join the new node. A control plane that leaves its cluster (deleting its `Membership`) takes itself out of the store first. Install control planes before they join: an installation carries the store of a single member, not of one member among others. Moving an existing cluster from kine to etcd is not automatic: create the new cluster on etcd and move workloads over (Velero, or the packages and their data).

A lab, not for clusters that matter: [a store in the model of Kubernetes resources](docs/resource-store.md), replicated by etcd's Raft library and speaking etcd's API, runs in etcd's place on an image built with `KUBEROOT_STORE_LAB=1` and booted with `kuberoot.datastore=kuberoot-store`. No image carries it otherwise.

## A fleet of edge nodes

Shops, towers and plants each run a node of their own, a cluster of one, often behind NAT and on a poor link. A fleet manages them from one central cluster, the center, without inbound ports on the nodes: every node calls the center and keeps the connection open, and the center reaches the node API through it, HTTP/2 inside the connection the node dialed. The center is `kuberoot-fleet`, which the edge control plane runs when booted with `kuberoot.fleet-center` (or `kuberoot.fleet-center=<host:port>,...` to name the address nodes reach it at through NAT); it applies its own resources, so any cluster can run it with a kubeconfig. The node side is part of the node API, nothing to install, no containers.

A node joins with a one-time token. Creating a `FleetNode` in the center issues it: a Secret in `kube-system` holds a ready `FleetEnrollment`, which an operator on site, or the boot media, applies to the node:

```yaml
apiVersion: fleet.kuberoot.dev/v1alpha1
kind: FleetNode
metadata: {name: shop-12, labels: {region: north, site: shop-12}}
---
# from Secret kube-system/fleet-enroll-shop-12, applied to the node itself
apiVersion: node.kuberoot.dev/v1alpha1
kind: FleetEnrollment
metadata: {name: center}
spec:
  name: shop-12
  servers: [fleet.example.org:7443]
  caHash: sha256:4769fc58...   # the center's authority, pinned
  token: ...                   # works once
```

Both ends are pinned. The node talks only to a center whose authority hashes to `caHash`. When the token is spent, the center pins the node's own authority, the one every kuberoot node has for its node API, and from then on opens the node's connection only for a client certificate that authority issued for the node's name. Deleting the FleetNode, or the node's FleetEnrollment, ends the membership.

The `FleetNode` is the inventory: whether the node is connected and since when, the release and slot it booted and their state, its services, health and when it was last seen, with labels for site, region or hardware (`fleet.kuberoot.dev/arch` and `/distro` are kept from what the node reports). `spec.release` is the release the node should run: whenever the node is connected the center stages it with the node's own `Upgrade` and reads the outcome back from its boot entries, so a node that ran out of boot attempts and went back to its previous slot is `RolledBack`, seen as such after any reboot or restart of the center, and not given that release again.

A `FleetRollout` sets the release node by node, in waves:

```yaml
apiVersion: fleet.kuberoot.dev/v1alpha1
kind: FleetRollout
metadata: {name: release-0.9.0}
spec:
  release:
    version: 0.9.0
    url: https://releases.example.org/kuberoot-0.9.0-amd64.tar
    sha256: f5fa3786...
  selector: {matchLabels: {region: north}}
  waves: ["1", "10%", "100%"]   # how many nodes have it once each wave is through
  maxFailures: 0
  offline: Wait                 # or Skip
  soak: 30m                     # optional: how long a wave runs before the next
```

The nodes are fixed when the rollout starts and taken in order of name. A wave starts once every node of the waves before it is updated (and has soaked, if asked). A node that rolls the release back or cannot stage it is a failure, and more than `maxFailures` stop the rollout: the nodes it has not reached keep what they run, and a stopped rollout starts again only when its spec changes. `paused: true` holds the next wave. A node that is offline when its wave comes holds the wave (`Wait`) or is left behind (`Skip`); either way its release is set and it takes it when it connects again. A node keeps running without the center; the center only acts when it hears from the node. A node belongs to the newest rollout that gave it a release.

The center also serves a connected node's API to its users, at `https://<center>:7443/nodes/<name>/`: `kubectl` with the center's client certificate and the center's authority (ConfigMap `kube-system/kuberoot-fleet-ca`, server name `kuberoot-fleet`) reaches the node's `nodeservices`, `bootentries` and the rest, for whoever the center's RBAC allows `get` on `fleetnodes/proxy`.

What it does not do yet: the center is one process, not highly available; the center acts on a node as a node admin, so whoever holds the center holds the fleet; tokens do not expire on their own (delete the FleetNode to revoke one); nodes added after a rollout started are not in it; the node's own cluster API (pods, `devices`) is not reached through the center, only its node API; and it was tried on four nodes, not thousands.

## Node API

| Resource | What it does |
|---|---|
| `osconfigs` | the node's operating system settings |
| `nodeservices` | the services kinit runs, their state and logs |
| `disks` | block devices |
| `installations` | writes the system onto a disk, from boot media |
| `bootentries` | the two root slots and which one boots |
| `upgrades` | stages a signed release into the inactive slot |
| `memberships`, `jointickets` | joins nodes to a cluster, as workers or control planes |
| `etcdmembers` | the members of the cluster store, on a control plane that runs etcd |
| `statebackups` | the control plane's state archives |
| `volumes`, `machines` | virtual machines' disks and machines (hypervisor) |
| `modelservers` | the language models the node serves (ai) |
| `latencytests` | the real-time latency the node delivers (rt) |
| `fleetenrollments` | makes the node a node of a fleet: it calls the center and keeps the connection open |

## Installing and upgrading

A node boots from media (`mkimage` writes a disk image with the kernel, the initramfs and the root filesystem). On the live system, an `Installation` partitions a disk: an EFI partition with systemd-boot, two root slots and a state partition. The node's identity, its membership of a cluster and, on a control plane, a snapshot of the cluster store move onto the disk with it.

An `Upgrade` downloads a signed release bundle, writes it to the inactive slot and boots it on probation: unless every service runs without restarting and the node is Ready within five minutes, the node reboots, and once the attempts run out it boots the previous slot again.

## Backups of the control plane

A control plane keeps its state in one place: the cluster store, its certificate authorities and its identity. The node API archives it every hour, and on request with `kubectl create statebackup`, keeps the latest twelve locally and, when the Secret `kube-system/kuberoot-state-backup` says so, uploads them to S3-compatible storage:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: kuberoot-state-backup
  namespace: kube-system
stringData:
  interval: 1h
  keep: "12"
  encryptionRecipient: age1...        # archives are encrypted to this age key
  s3Endpoint: https://s3.example.org
  s3Bucket: backups
  s3AccessKey: ...
  s3SecretKey: ...
```

Archives hold the cluster's keys and every Secret, so they leave the node only encrypted (`age-keygen` makes a key pair; the secret key stays with you). To bring a lost control plane back on new hardware, boot the new machine from media and install it from a backup:

```yaml
apiVersion: node.kuberoot.dev/v1alpha1
kind: Installation
metadata:
  name: restore
spec:
  disk: vda
  reboot: true
  restore:
    url: https://...      # a presigned link to the archive
    sha256: ...           # as the StateBackup reports it
    identity: AGE-SECRET-KEY-1...
```

The node comes back as the same node, with the same name, certificates and cluster. On etcd, the archive holds a snapshot of the store, and the restored node starts the store from it as its only member; the other control planes join it again.

## Layout

| Path | |
|---|---|
| `cmd/kinit` | PID 1 |
| `cmd/kuberoot-node` | the node API server |
| `cmd/kuberoot-installer` | the installer's console interface |
| `cmd/kuberoot-intents` | lowers typed intents to Kubernetes primitives |
| `cmd/kuberoot-router` | the router controller |
| `cmd/kuberoot-vmctl` | the virtual machines controller |
| `cmd/kuberoot-aictl` | the models controller and endpoint |
| `cmd/kuberoot-devices` | the devices controller of the iot distribution |
| `cmd/kuberoot-wsctl` | the workspaces controller and their gateway |
| `cmd/kuberoot-fleet` | the center of a fleet of nodes |
| `cmd/mkimage`, `cmd/kuberoot-release` | boot media and signed releases |
| `pkg/` | the libraries behind them |
| `distros/` | the distributions: services, modules, add-ons, packages |
| `kernel/` | kernel configuration and versions |
| `rootfs/` | files of the root filesystem |
| `scripts/` | the build steps |

## Building

Builds run in a container with the toolchain, not on the workstation: `scripts/` hold the steps (kernel, DRBD, binaries, root filesystem, initramfs, bundle). `hack/hetzner.sh` drives a builder pod in a Kubernetes cluster and runs them there; `hack/hetzner/` has the builder, a registry and the VM definitions the author tests nodes with.

```sh
hack/hetzner.sh build amd64      # kernel if missing, binaries, root filesystem, initramfs
hack/hetzner.sh bundle amd64     # a signed release bundle
hack/hetzner.sh media amd64      # boot media
hack/hetzner.sh test             # go test ./...
```

## License

Apache License 2.0, see [LICENSE](LICENSE).
