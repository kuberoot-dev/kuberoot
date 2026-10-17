package nodeapi

import (
	"context"
	cryptox509 "crypto/x509"
	"fmt"
	"net"
	"net/http"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/request/headerrequest"
	"k8s.io/apiserver/pkg/authentication/request/union"
	"k8s.io/apiserver/pkg/authentication/request/x509"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	"k8s.io/apiserver/pkg/authorization/authorizerfactory"
	authorizerunion "k8s.io/apiserver/pkg/authorization/union"
	openapinamer "k8s.io/apiserver/pkg/endpoints/openapi"
	"k8s.io/apiserver/pkg/registry/rest"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"k8s.io/apiserver/pkg/server/dynamiccertificates"
	genericoptions "k8s.io/apiserver/pkg/server/options"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	basecompatibility "k8s.io/component-base/compatibility"
	"k8s.io/klog/v2"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
	"github.com/kuberoot-dev/kuberoot/pkg/apis/node/install"
	fleetagent "github.com/kuberoot-dev/kuberoot/pkg/fleet"
	"github.com/kuberoot-dev/kuberoot/pkg/generated/openapi"
	"github.com/kuberoot-dev/kuberoot/pkg/gpu"
	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
)

// AdminGroup is the client certificate organization that may manage the node.
const AdminGroup = "kuberoot:node-admins"

type Options struct {
	NodeName   string
	BindPort   int
	CertFile   string
	KeyFile    string
	ClientCA   string
	Kubeconfig map[string]string // name -> file of cluster credentials to hand out
	// PKIDir holds the node's own authority, node-ca, which a node of a
	// fleet proves itself to the center with.
	PKIDir string

	// Aggregation into the cluster API server; all optional, the node API
	// works standalone without them.
	RequestHeaderCA   string // front proxy CA the cluster API server forwards requests with
	ClusterCertFile   string // serving certificate signed by the cluster CA, chosen by SNI
	ClusterKeyFile    string
	ClusterKubeconfig string // identity for SubjectAccessReviews against the cluster

	Advertise string // this node's address in the cluster

	// Control plane node: issues join tickets and serves the fleet view.
	ClusterCAFile    string
	ClusterCAKeyFile string
	AdminKubeconfig  string
	ProxyClientCert  string // identity for reaching member node APIs
	ProxyClientKey   string

	// Member node: trusts the control plane's proxy identity from this CA.
	ProxyTrustCA string

	// PodCIDR and ServiceCIDR of the cluster, handed to joining nodes.
	PodCIDR string
	// PodNetwork is how pod traffic crosses between nodes: PodNetworkVXLAN
	// or PodNetworkHostGW.
	PodNetwork  string
	ServiceCIDR string

	// Resources the distribution serves; empty serves all.
	Resources []string

	// Machines: the node runs virtual machines (volumes and machines
	// resources), on a network joined to the nodes VMNetworkKubeconfig lists.
	Machines            bool
	VMNetworkKubeconfig string

	// ModelServers: the node serves language models (modelservers resource),
	// with all its memory but ModelReserveMiB for them.
	ModelServers    bool
	ModelReserveMiB int64

	// GPUs: make the node's NVIDIA GPUs usable, device files and their CDI
	// description for the container runtime.
	GPUs bool

	// LatencyTests: the node measures the latency it delivers to real-time
	// tasks (latencytests resource).
	LatencyTests bool

	// RoutesKubeconfig reads the nodes to route pod subnets between them.
	RoutesKubeconfig string

	// Etcd: this control plane runs a member of the cluster store, which
	// other control planes join (etcdmembers, ControlPlane join tickets).
	Etcd bool
	// APIServers are the cluster's API servers, for a worker of several
	// control planes: the node balances between them on APIBalancer,
	// following them with APIKubeconfig.
	APIServers    []string
	APIBalancer   string
	APIKubeconfig string
}

var (
	scheme = runtime.NewScheme()
	codecs = serializer.NewCodecFactory(scheme)
)

func init() {
	install.Install(scheme)
	metav1.AddToGroupVersion(scheme, schema.GroupVersion{Version: "v1"})
	unversioned := schema.GroupVersion{Group: "", Version: "v1"}
	scheme.AddUnversionedTypes(unversioned,
		&metav1.Status{}, &metav1.APIVersions{}, &metav1.APIGroupList{}, &metav1.APIGroup{}, &metav1.APIResourceList{})
}

// Run serves the node API until ctx is cancelled.
func Run(ctx context.Context, o Options) error {
	cfg := genericapiserver.NewRecommendedConfig(codecs)
	cfg.EffectiveVersion = basecompatibility.NewEffectiveVersionFromString("1.37", "", "")

	serving := genericoptions.NewSecureServingOptions().WithLoopback()
	serving.BindPort = o.BindPort
	serving.ServerCert.CertKey = genericoptions.CertKey{CertFile: o.CertFile, KeyFile: o.KeyFile}
	if err := serving.ApplyTo(&cfg.SecureServing, &cfg.LoopbackClientConfig); err != nil {
		return fmt.Errorf("serving: %w", err)
	}

	if err := configureAuth(cfg, o); err != nil {
		return err
	}
	genericapiserver.AuthorizeClientBearerToken(cfg.LoopbackClientConfig, &cfg.Authentication, &cfg.Authorization)

	namer := openapinamer.NewDefinitionNamer(scheme)
	cfg.OpenAPIConfig = genericapiserver.DefaultOpenAPIConfig(openapi.GetOpenAPIDefinitions, namer)
	cfg.OpenAPIConfig.Info.Title = "kuberoot node"
	cfg.OpenAPIV3Config = genericapiserver.DefaultOpenAPIV3Config(openapi.GetOpenAPIDefinitions, namer)
	cfg.OpenAPIV3Config.Info.Title = "kuberoot node"

	chain := cfg.BuildHandlerChainFunc
	cfg.BuildHandlerChainFunc = func(h http.Handler, c *genericapiserver.Config) http.Handler {
		return chain(withRequestHost(h), c)
	}

	server, err := cfg.Complete().New("kuberoot-node", genericapiserver.NewEmptyDelegate())
	if err != nil {
		return err
	}
	kinit := supervisor.NewClient()
	// The fleet agent calls a fleet's center once the node is enrolled.
	agent := &fleetagent.Agent{StateDir: "/var/lib/kuberoot/fleet", PKIDir: o.PKIDir,
		LocalAPI: fmt.Sprintf("https://127.0.0.1:%d", o.BindPort), AdminGroup: AdminGroup}
	storages := enabledResources(o.Resources, resourceDeps{nodeName: o.NodeName, kinit: kinit, fleet: agent})
	var vms *vmHost
	if o.Machines {
		vms = newVMHost(o.NodeName, o.Advertise)
		storages["volumes"], storages["machines"] = volumeStorage{vms}, machineStorage{vms}
	}
	var models *modelHost
	if o.ModelServers {
		models = newModelHost(o.ModelReserveMiB << 20)
		storages["modelservers"] = modelServerStorage{models}
	}
	var latency *latencyTests
	if o.LatencyTests {
		latency = newLatencyTests("/var/lib/kuberoot/latencytests")
		storages["latencytests"] = latencyStorage{latency}
	}
	logs := &logStorage{kinit: kinit}
	resources := map[string]rest.Storage{
		"kubeconfigs": &kubeconfigStorage{files: o.Kubeconfig},
	}
	if storages["nodeservices"] != nil {
		resources["nodeservices/log"] = logs
	}
	if vms != nil {
		resources["machines/console"] = consoleStorage{vms}
	}
	if models != nil {
		resources["modelservers/log"] = modelServerLog{models}
	}
	if o.AdminKubeconfig != "" {
		admin := clientFrom(o.AdminKubeconfig)
		if admin == nil {
			return fmt.Errorf("admin kubeconfig %s is not usable", o.AdminKubeconfig)
		}
		f, err := newFleet(o.NodeName, admin, o.ClusterCAFile, o.ProxyClientCert, o.ProxyClientKey)
		if err != nil {
			return fmt.Errorf("fleet: %w", err)
		}
		for name, st := range storages {
			storages[name] = &fleetStorage{resource: name, local: st, f: f, singleton: name == "osconfigs"}
		}
		if storages["nodeservices"] != nil {
			resources["nodeservices/log"] = &fleetLog{local: logs, f: f, resource: "nodeservices", sub: "log"}
		}
		if vms != nil {
			resources["machines/console"] = &fleetLog{local: consoleStorage{vms}, f: f, resource: "machines", sub: "console"}
		}
		if models != nil {
			resources["modelservers/log"] = &fleetLog{local: modelServerLog{models}, f: f, resource: "modelservers", sub: "log"}
		}
		tickets, err := newJoinTickets(admin, o)
		if err != nil {
			return fmt.Errorf("join tickets: %w", err)
		}
		resources["jointickets"] = tickets
		if o.Etcd {
			// The store's members are the cluster's, the same seen from any
			// control plane: served as they are, not node by node.
			resources["etcdmembers"] = etcdMemberStorage{}
			resources["etcdrotations"] = etcdRotationStorage{}
			resources["etcdcertificates"] = etcdCertificateStorage{}
			go promoteLearners(ctx)
			go (&etcdRotator{name: o.NodeName, kinit: kinit, admin: admin}).run(ctx)
		}
		var reserved []*net.IPNet
		for _, c := range []string{o.PodCIDR, o.ServiceCIDR} {
			if _, r, err := net.ParseCIDR(c); err == nil {
				reserved = append(reserved, r)
			}
		}
		go approveKubeletServing(ctx, admin, reserved)
		// The control plane backs its state up; members hold none of their own.
		backups := newStateBackups(o.NodeName, admin)
		resources["statebackups"] = backups
		go backups.run(ctx)
	}
	for name, st := range storages {
		resources[name] = st
	}
	group := genericapiserver.NewDefaultAPIGroupInfo(node.GroupName, scheme, metav1.ParameterCodec, codecs)
	group.VersionedResourcesStorageMap["v1alpha1"] = resources
	if err := server.InstallAPIGroup(&group); err != nil {
		return err
	}
	go assessBoot(ctx, kinit, o.NodeName)
	if storages["fleetenrollments"] != nil {
		go agent.Run(ctx)
	}
	if latency != nil {
		go latency.run(ctx)
	}
	if models != nil {
		go models.run(ctx)
	}
	if len(o.APIServers) > 0 && o.APIBalancer != "" {
		b := newAPIBalancer(o.APIServers)
		go func() {
			if err := b.serve(ctx, o.APIBalancer); err != nil {
				klog.Errorf("API balancer: %v", err)
			}
		}()
		if o.APIKubeconfig != "" {
			go b.follow(ctx, o.APIKubeconfig)
		}
	}
	if o.GPUs {
		go setupGPUs(ctx)
	}
	if vms != nil {
		go vms.run(ctx)
		if o.VMNetworkKubeconfig != "" {
			go vms.runNetwork(ctx, o.VMNetworkKubeconfig, net.ParseIP(o.Advertise))
		}
	}
	if o.RoutesKubeconfig != "" {
		_, podRange, _ := net.ParseCIDR(o.PodCIDR)
		if o.PodNetwork != PodNetworkNone {
			go syncRoutes(ctx, o.RoutesKubeconfig, o.NodeName, podRange, o.PodNetwork)
		}
	}
	return server.PrepareRun().RunWithContext(ctx)
}

// configureAuth accepts two kinds of callers: node admins with a certificate of
// the node CA, and cluster users forwarded by the cluster API server, whose
// permissions the cluster's RBAC decides.
func configureAuth(cfg *genericapiserver.RecommendedConfig, o Options) error {
	nodeCA, err := dynamiccertificates.NewDynamicCAContentFromFile("node-client-ca", o.ClientCA)
	if err != nil {
		return fmt.Errorf("client CA: %w", err)
	}
	authenticators := []authenticator.Request{x509.NewDynamic(nodeCA.VerifyOptions, x509.CommonNameUserConversion)}
	authorizers := []authorizerunion.NamedAuthorizer{{AuthorizerName: "node-admins", Authorizer: authorizer.AuthorizerFunc(authorize)}}
	clientCAs := []dynamiccertificates.CAContentProvider{nodeCA}

	if o.RequestHeaderCA != "" {
		frontProxy, err := dynamiccertificates.NewDynamicCAContentFromFile("front-proxy-ca", o.RequestHeaderCA)
		if err != nil {
			return fmt.Errorf("request header CA: %w", err)
		}
		clientCAs = append(clientCAs, frontProxy)
		authenticators = append(authenticators, markViaCluster(headerrequest.NewDynamicVerifyOptionsSecure(
			frontProxy.VerifyOptions,
			headerrequest.StaticStringSlice{"front-proxy-client"},
			headerrequest.StaticStringSlice{"X-Remote-User"},
			headerrequest.StaticStringSlice{"X-Remote-Uid"},
			headerrequest.StaticStringSlice{"X-Remote-Group"},
			headerrequest.StaticStringSlice{"X-Remote-Extra-"},
		)))
	}
	if o.ProxyTrustCA != "" {
		clusterCA, err := dynamiccertificates.NewDynamicCAContentFromFile("cluster-ca", o.ProxyTrustCA)
		if err != nil {
			return fmt.Errorf("proxy trust CA: %w", err)
		}
		clientCAs = append(clientCAs, clusterCA)
		authenticators = append(authenticators, x509.NewDynamic(clusterCA.VerifyOptions, controlPlaneProxy))
	}
	if o.ClusterCertFile != "" {
		sni, err := dynamiccertificates.NewDynamicSNIContentFromFiles("cluster-serving", o.ClusterCertFile, o.ClusterKeyFile)
		if err != nil {
			return fmt.Errorf("cluster serving certificate: %w", err)
		}
		cfg.SecureServing.SNICerts = append(cfg.SecureServing.SNICerts, sni)
	}
	if o.ClusterKubeconfig != "" {
		delegated, err := delegatedAuthorizer(o.ClusterKubeconfig)
		if err != nil {
			return err
		}
		authorizers = append(authorizers, authorizerunion.NamedAuthorizer{AuthorizerName: "cluster", Authorizer: delegated})
	}
	cfg.SecureServing.ClientCA = dynamiccertificates.NewUnionCAContentProvider(clientCAs...)
	cfg.Authentication.Authenticator = union.New(authenticators...)
	authz, err := authorizerunion.New(authorizers...)
	if err != nil {
		return err
	}
	cfg.Authorization.Authorizer = authz
	return nil
}

// controlPlaneProxy admits the control plane node API, which has already
// authorized the cluster user before forwarding to this member.
var controlPlaneProxy = x509.UserConversionFunc(func(chain []*cryptox509.Certificate) (*authenticator.Response, bool, error) {
	if chain[0].Subject.CommonName != "kuberoot:node-api-proxy" {
		return nil, false, nil
	}
	return &authenticator.Response{User: &user.DefaultInfo{Name: "kuberoot:node-api-proxy", Groups: []string{AdminGroup}}}, true, nil
})

// delegatedAuthorizer asks the cluster whether a forwarded user may do something.
// The kubeconfig may point at a cluster that is not up yet; checks fail until it is.
func delegatedAuthorizer(kubeconfig string) (authorizer.Authorizer, error) {
	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("cluster kubeconfig: %w", err)
	}
	client, err := kubernetes.NewForConfig(restCfg)
	if err != nil {
		return nil, err
	}
	return authorizerfactory.DelegatingAuthorizerConfig{
		SubjectAccessReviewClient: client.AuthorizationV1(),
		AllowCacheTTL:             10 * time.Second,
		DenyCacheTTL:              10 * time.Second,
		WebhookRetryBackoff:       genericoptions.DefaultAuthWebhookRetryBackoff(),
	}.New()
}

// authorize lets node admins do everything and leaves everyone else to the cluster.
func authorize(_ context.Context, a authorizer.Attributes) (authorizer.Decision, string, error) {
	for _, g := range a.GetUser().GetGroups() {
		if g == AdminGroup || g == "system:masters" {
			return authorizer.DecisionAllow, "", nil
		}
	}
	return authorizer.DecisionNoOpinion, "not a node admin", nil
}

func withRequestHost(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestHostKey{}, r.Host)))
	})
}

// setupGPUs makes the GPUs usable, trying again while the driver comes up.
func setupGPUs(ctx context.Context) {
	for i := 0; i < 30 && ctx.Err() == nil; i++ {
		gpus, err := gpu.Setup()
		if err == nil {
			if len(gpus) > 0 {
				klog.Infof("GPUs: %d ready for containers", len(gpus))
			}
			return
		}
		klog.Errorf("GPUs: %v", err)
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Second):
		}
	}
}
