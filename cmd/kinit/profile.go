package main

import (
	"bytes"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/tym83/kuberoot/pkg/distro"
)

// profilePath is the distribution profile built into the image: what this
// distribution's node roles generate and run. kinit itself knows no services.
const profilePath = distro.Path

type (
	profile     = distro.Profile
	roleSpec    = distro.Role
	serviceSpec = distro.Service
)

// Role names in a profile.
const (
	roleControlPlane = distro.RoleControlPlane
	roleWorker       = distro.RoleWorker
)

// activeProfile is the distribution profile this boot runs.
var activeProfile *profile

// rescueProfile runs only the node API: when the image's profile cannot be
// read, the node stays reachable and can be upgraded to a working release.
const rescueProfile = `apiVersion: kuberoot.dev/v1alpha1
kind: Distribution
metadata: {name: rescue}
spec:
  roles:
    controlPlane:
      generators: [node]
      services:
      - name: kuberoot-node
        args: [/usr/bin/kuberoot-node, '--node-name={{.Node.Name}}', '--advertise-address={{.Node.IP}}',
          '--tls-cert-file={{pki "node-api.crt"}}', '--tls-private-key-file={{pki "node-api.key"}}',
          '--client-ca-file={{pki "node-client-ca.crt"}}']
`

func loadProfileOrRescue() *profile {
	p, err := loadProfile()
	if err == nil {
		log.Printf("distribution %s", p.Metadata.Name)
		return p
	}
	log.Printf("distribution profile: %v; running the rescue profile (node API only)", err)
	p, err = distro.Parse([]byte(rescueProfile), "rescue profile")
	if err != nil {
		panic(err)
	}
	return p
}

func loadProfile() (*profile, error) { return distro.Load(profilePath) }

func loadProfileFile(path string) (*profile, error) { return distro.Load(path) }

// facts are what service templates see.
type facts struct {
	Node struct{ Name, IP string }
	Net  struct{ Pod, Service, DNS, PodNetwork string }
	Boot struct {
		Distro        string
		RepoPlainHTTP bool
	}
	// Store is the cluster store of a control plane, Etcd its member.
	Store storeFacts
	Etcd  etcdFacts
	// Member: Servers are the cluster's API servers, comma-separated, for a
	// worker that balances between several control planes.
	Member struct{ Servers string }
}

func roleFacts(r roleContext, cfg bootConfig) facts {
	var f facts
	f.Store, f.Etcd = newStoreFacts(r.store, r.node)
	if r.member != nil {
		f.Member.Servers = strings.Join(r.member.Servers, ",")
	}
	f.Node.Name, f.Node.IP = r.node.name, r.node.ip.String()
	f.Net.Pod, f.Net.Service = r.net.pod.String(), r.net.service.String()
	f.Net.DNS = r.net.dnsIP().String()
	f.Net.PodNetwork = cfg.podNetwork
	f.Boot.Distro, f.Boot.RepoPlainHTTP = distroName(cfg), cfg.repoPlainHTTP
	return f
}

var templateFuncs = template.FuncMap{
	"pki":     pkiPath,
	"kube":    func(name string) string { return filepath.Join(kubeDir, name) },
	"cluster": func(name string) string { return filepath.Join(clusterDir, name) },
}

var afterConditions = map[string]func() bool{
	"apiserver":    apiServerReady,
	"kubelet-cert": kubeletCertIssued,
}

// renderRole runs a role's generators and renders its services.
func renderRole(spec roleSpec, r roleContext, cfg bootConfig) ([]service, error) {
	for _, g := range spec.Generators {
		gen, ok := generators[g]
		if !ok {
			return nil, fmt.Errorf("unknown generator %q", g)
		}
		if err := gen(r); err != nil {
			return nil, fmt.Errorf("generator %s: %w", g, err)
		}
	}
	return renderServices(spec, roleFacts(r, cfg))
}

// renderServices turns a role's service templates into services.
func renderServices(spec roleSpec, f facts) ([]service, error) {
	var services []service
	for _, s := range spec.Services {
		if s.When != "" {
			on, err := render(s.When, f)
			if err != nil {
				return nil, fmt.Errorf("service %s, when: %w", s.Name, err)
			}
			if on != "true" {
				continue
			}
		}
		svc := service{name: s.Name, env: s.Env}
		if s.After != "" {
			cond, ok := afterConditions[s.After]
			if !ok {
				return nil, fmt.Errorf("service %s: unknown after %q", s.Name, s.After)
			}
			svc.after = cond
		}
		for i, a := range s.Args {
			out, err := render(a, f)
			if err != nil {
				return nil, fmt.Errorf("service %s, argument %d: %w", s.Name, i, err)
			}
			if out != "" {
				svc.args = append(svc.args, out)
			}
		}
		if len(svc.args) == 0 {
			return nil, fmt.Errorf("service %s has no command", s.Name)
		}
		services = append(services, svc)
	}
	return services, nil
}

func render(text string, f facts) (string, error) {
	t, err := template.New("").Funcs(templateFuncs).Option("missingkey=error").Parse(text)
	if err != nil {
		return "", err
	}
	var b bytes.Buffer
	if err := t.Execute(&b, f); err != nil {
		return "", err
	}
	return strings.TrimSpace(b.String()), nil
}
