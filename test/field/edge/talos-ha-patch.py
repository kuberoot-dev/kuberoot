#!/usr/bin/env python3
"""Emit public networking patch for one of three Talos field-test control planes.
Schema references are linked in README; talosctl validates the merged private file.
"""
import sys
id=int(sys.argv[1]);assert id in [1,2,3]
print(f'''version: v1alpha1
cluster:
  etcd:
    advertisedSubnets: [10.79.0.0/24]
    listenSubnets: [10.79.0.0/24]
---
apiVersion: v1alpha1
kind: HostnameConfig
auto:
  $patch: delete
hostname: field-talos-ha-{id}
---
apiVersion: v1alpha1
kind: LinkAliasConfig
name: field-peer
selector:
  match: mac(link.permanent_addr) == "52:54:00:79:00:0{id}"
---
apiVersion: v1alpha1
kind: LinkConfig
name: field-peer
up: true
addresses:
  - address: 10.79.0.1{id}/24
---
apiVersion: v1alpha1
kind: KubeNodeConfig
nodeIP:
  validSubnets: [10.79.0.0/24]
taints:
  node-role.kubernetes.io/control-plane:
    $patch: delete
---
apiVersion: v1alpha1
kind: KubeAPIServerConfig
extraArgs:
  advertise-address: 10.79.0.1{id}
---
apiVersion: v1alpha1
kind: KubeFlannelCNIConfig
extraArgs:
  - --iface-can-reach=10.79.0.1
''')
