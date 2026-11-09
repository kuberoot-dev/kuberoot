// Command kuberoot-vm-api-migrate prepares and executes the explicit VM API
// maintenance cutover. Plans contain Secrets and are written privately.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/yaml"

	"github.com/kuberoot-dev/kuberoot/pkg/vmapimigrate"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	kubeconfig := flag.String("kubeconfig", "", "explicit management-cluster kubeconfig")
	contextName := flag.String("context", "", "explicit kubeconfig context")
	mode := flag.String("mode", "verify", "prepare, verify, cutover or resume")
	planPath := flag.String("plan", "", "private immutable plan/backup file")
	statePath := flag.String("state", "", "private durable cutover checkpoint file")
	hash := flag.String("plan-sha256", "", "explicit SHA256 of the reviewed plan, required for cutover/resume")
	apiServerMap := flag.String("api-server-endpoints", "", "JSON map of every control-plane node to its direct https://InternalIP:port listener; no proxy/load balancer")
	namespaceMap := flag.String("namespace-map", "", "JSON object mapping every legacy VM name to its target namespace")
	targetCRD := flag.String("target-crd", "", "generated v1alpha2 VM CRD YAML")
	execute := flag.Bool("execute-cutover", false, "explicitly delete the backed-up VM API objects and replace their CRD")
	resume := flag.Bool("resume-controllers", false, "explicitly release the fences after verified import")
	timeout := flag.Duration("timeout", 5*time.Minute, "maximum duration; failure leaves the maintenance fences in place")
	flag.Parse()
	if *kubeconfig == "" || *contextName == "" || *planPath == "" || *apiServerMap == "" {
		return fmt.Errorf("--kubeconfig, --context, --plan and --api-server-endpoints are required")
	}
	if *mode != "prepare" && *mode != "verify" && *mode != "cutover" && *mode != "resume" {
		return fmt.Errorf("unknown mode %q", *mode)
	}
	if *mode == "cutover" && (!*execute || *hash == "" || *statePath == "") {
		return fmt.Errorf("cutover requires --execute-cutover, --plan-sha256 and --state")
	}
	if *mode == "resume" && (!*resume || *hash == "" || *statePath == "") {
		return fmt.Errorf("resume requires --resume-controllers, --plan-sha256 and --state")
	}
	if *execute && *mode != "cutover" || *resume && *mode != "resume" {
		return fmt.Errorf("execution flags must match their explicit mode")
	}
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(&clientcmd.ClientConfigLoadingRules{ExplicitPath: *kubeconfig}, &clientcmd.ConfigOverrides{CurrentContext: *contextName}).ClientConfig()
	if err != nil {
		return err
	}
	if config.TLSClientConfig.Insecure {
		return fmt.Errorf("direct API admission probes require verified TLS; insecure-skip-tls-verify is not permitted")
	}
	config.Timeout = 20 * time.Second
	kube, err := kubernetes.NewForConfig(config)
	if err != nil {
		return err
	}
	dyn, err := dynamic.NewForConfig(config)
	if err != nil {
		return err
	}
	discover, err := discovery.NewDiscoveryClientForConfig(config)
	if err != nil {
		return err
	}
	endpointRaw, err := os.ReadFile(*apiServerMap)
	if err != nil {
		return err
	}
	endpoints := map[string]string{}
	if err = json.Unmarshal(endpointRaw, &endpoints); err != nil {
		return err
	}
	client := &vmapimigrate.Client{Kube: kube, Dynamic: dyn, Discovery: discover, APIServers: endpoints,
		ProbeClient: func(endpoint, user string) (dynamic.Interface, error) {
			probe := rest.CopyConfig(config)
			probe.Host = endpoint
			probe.TLSClientConfig.ServerName = ""
			probe.Timeout = 5 * time.Second
			probe.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
			probe.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: []string{"system:authenticated"}}
			return dynamic.NewForConfig(probe)
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	if *mode == "prepare" {
		if _, err = os.Stat(*planPath); err == nil {
			p, digest, err := vmapimigrate.LoadPlan(*planPath, *hash)
			if err != nil {
				return err
			}
			if err = client.VerifyUnchanged(ctx, p); err != nil {
				return err
			}
			fmt.Printf("Existing private plan verified: %s\nSHA256 %s\n", *planPath, digest)
			return nil
		} else if !os.IsNotExist(err) {
			return err
		}
		if *namespaceMap == "" || *targetCRD == "" {
			return fmt.Errorf("prepare requires --namespace-map and --target-crd")
		}
		raw, err := os.ReadFile(*namespaceMap)
		if err != nil {
			return err
		}
		mapping := map[string]string{}
		if err = json.Unmarshal(raw, &mapping); err != nil {
			return err
		}
		raw, err = os.ReadFile(*targetCRD)
		if err != nil {
			return err
		}
		target := &unstructured.Unstructured{}
		if err = yaml.Unmarshal(raw, &target.Object); err != nil {
			return err
		}
		p, err := client.Prepare(ctx, mapping, target)
		if err != nil {
			return fmt.Errorf("prepare stopped; leave the maintenance fences in place and resolve the precondition: %w", err)
		}
		if err = vmapimigrate.SavePrivate(*planPath, p); err != nil {
			return err
		}
		_, digest, err := vmapimigrate.LoadPlan(*planPath, "")
		if err != nil {
			return err
		}
		fmt.Printf("Private plan saved: %s\nSHA256 %s\nNo VM API objects or node resources were deleted. Review the plan before explicit cutover.\n", *planPath, digest)
		return nil
	}
	p, digest, err := vmapimigrate.LoadPlan(*planPath, *hash)
	if err != nil {
		return err
	}
	switch *mode {
	case "verify":
		err = client.Verify(ctx, p, digest, *statePath)
	case "cutover":
		err = client.Cutover(ctx, p, digest, *statePath)
	case "resume":
		err = client.Resume(ctx, p, digest, *statePath)
	}
	if err != nil {
		return fmt.Errorf("%s stopped; do not remove the maintenance fences manually: %w", *mode, err)
	}
	fmt.Printf("%s verified for plan SHA256 %s\n", *mode, digest)
	return nil
}
