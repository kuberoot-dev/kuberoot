package nodeapi

import (
	"sort"

	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
)

// resourceDeps is what a node resource is built from.
type resourceDeps struct {
	nodeName string
	kinit    *supervisor.Client
}

// nodeResources are the per-node resource types the node API can serve, by
// plural name. A distribution profile picks the ones it serves; a type for a
// new kind of node, say network interfaces for a router, registers itself here.
var nodeResources = map[string]func(resourceDeps) rest.Storage{
	"osconfigs":     func(d resourceDeps) rest.Storage { return newOSConfigStorage(d.nodeName, d.kinit) },
	"nodeservices":  func(d resourceDeps) rest.Storage { return newServiceStorage(d.kinit) },
	"disks":         func(resourceDeps) rest.Storage { return diskStorage{} },
	"installations": func(d resourceDeps) rest.Storage { return &installationStorage{kinit: d.kinit} },
	"bootentries":   func(resourceDeps) rest.Storage { return bootEntryStorage{} },
	"upgrades":      func(d resourceDeps) rest.Storage { return &upgradeStorage{kinit: d.kinit} },
	"memberships":   func(d resourceDeps) rest.Storage { return &membershipStorage{kinit: d.kinit, nodeName: d.nodeName} },
}

// enabledResources builds the resources a profile asks for; none listed means all.
func enabledResources(names []string, d resourceDeps) map[string]rest.Storage {
	if len(names) == 0 {
		for name := range nodeResources {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	out := map[string]rest.Storage{}
	for _, name := range names {
		if build, ok := nodeResources[name]; ok {
			out[name] = build(d)
		}
	}
	return out
}
