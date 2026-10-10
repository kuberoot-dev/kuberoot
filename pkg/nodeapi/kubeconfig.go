package nodeapi

import (
	"context"
	"net"
	"os"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apiserver/pkg/registry/rest"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

// kubeconfigStorage hands out credentials for the cluster API server, so the
// node API is the only thing a new owner needs to reach first.
type kubeconfigStorage struct {
	files map[string]string // name -> kubeconfig file on the node
}

var (
	_ rest.Getter = &kubeconfigStorage{}
	_ rest.Lister = &kubeconfigStorage{}
)

func (k *kubeconfigStorage) New() runtime.Object     { return &node.Kubeconfig{} }
func (k *kubeconfigStorage) NewList() runtime.Object { return &node.KubeconfigList{} }
func (k *kubeconfigStorage) Destroy()                {}
func (k *kubeconfigStorage) NamespaceScoped() bool   { return false }
func (k *kubeconfigStorage) GetSingularName() string { return "kubeconfig" }

func (k *kubeconfigStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	path, ok := k.files[name]
	if !ok {
		return nil, apierrors.NewNotFound(node.Resource("kubeconfigs"), name)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, apierrors.NewServiceUnavailable("kubeconfig not issued yet")
	}
	server := clusterServerFor(ctx)
	content := string(raw)
	for _, line := range strings.Split(content, "\n") {
		if old, ok := strings.CutPrefix(strings.TrimSpace(line), "server: "); ok {
			content = strings.Replace(content, "server: "+old, "server: "+server, 1)
		}
	}
	return &node.Kubeconfig{
		ObjectMeta: metav1.ObjectMeta{Name: name, UID: nodeUID() + "-kubeconfig-" + "admin", ResourceVersion: resourceVersion(content)},
		Server:     server,
		Kubeconfig: content,
	}, nil
}

func (k *kubeconfigStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	list := &node.KubeconfigList{}
	for name := range k.files {
		obj, err := k.Get(ctx, name, nil)
		if err != nil {
			continue
		}
		list.Items = append(list.Items, *obj.(*node.Kubeconfig))
	}
	return list, nil
}

// clusterServerFor points the kubeconfig at the address the client used to reach
// the node API: if the client got here, the cluster API on :6443 is reachable too.
func clusterServerFor(ctx context.Context) string {
	host, _ := ctx.Value(requestHostKey{}).(string)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		host = "127.0.0.1"
	}
	return "https://" + net.JoinHostPort(host, "6443")
}

type requestHostKey struct{}

func (k *kubeconfigStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.Kubeconfig:
		items = []runtime.Object{o}
	case *node.KubeconfigList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("Server", "")}, items,
		func(o runtime.Object) []any { kc := o.(*node.Kubeconfig); return []any{kc.Name, kc.Server} }), nil
}
