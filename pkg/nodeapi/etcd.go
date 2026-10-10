package nodeapi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metainternalversion "k8s.io/apimachinery/pkg/apis/meta/internalversion"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apiserver/pkg/registry/rest"
	"k8s.io/klog/v2"

	"github.com/tym83/kuberoot/pkg/apis/node"
	nodev1 "github.com/tym83/kuberoot/pkg/apis/node/v1alpha1"
	"github.com/tym83/kuberoot/pkg/etcdstore"
)

// etcdMemberStorage serves the members of the cluster store on a control
// plane that runs one; deleting a member removes it from the store.
type etcdMemberStorage struct{}

var _ rest.GracefulDeleter = etcdMemberStorage{}

func (etcdMemberStorage) New() runtime.Object     { return &node.EtcdMember{} }
func (etcdMemberStorage) NewList() runtime.Object { return &node.EtcdMemberList{} }
func (etcdMemberStorage) Destroy()                {}
func (etcdMemberStorage) NamespaceScoped() bool   { return false }
func (etcdMemberStorage) GetSingularName() string { return "etcdmember" }

func (etcdMemberStorage) list(ctx context.Context) (*node.EtcdMemberList, error) {
	c, err := etcdstore.Client()
	if err != nil {
		return nil, apierrors.NewInternalError(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	members, leader, err := etcdstore.Members(ctx, c)
	if err != nil {
		return nil, apierrors.NewServiceUnavailable("cluster store: " + err.Error())
	}
	list := &node.EtcdMemberList{}
	for _, m := range members {
		name := m.Name
		if name == "" {
			name = "unstarted-" + strconv.FormatUint(m.ID, 16)
		}
		item := node.EtcdMember{
			ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID("etcd-member-" + strconv.FormatUint(m.ID, 16))},
			Status: node.EtcdMemberStatus{ID: strconv.FormatUint(m.ID, 16), PeerURLs: m.PeerURLs, ClientURLs: m.ClientURLs,
				Learner: m.IsLearner, Leader: m.ID == leader},
		}
		item.ResourceVersion = resourceVersion(item.Status)
		list.Items = append(list.Items, item)
	}
	return list, nil
}

func (s etcdMemberStorage) List(ctx context.Context, _ *metainternalversion.ListOptions) (runtime.Object, error) {
	return s.list(ctx)
}

func (s etcdMemberStorage) Get(ctx context.Context, name string, _ *metav1.GetOptions) (runtime.Object, error) {
	list, err := s.list(ctx)
	if err != nil {
		return nil, err
	}
	for i := range list.Items {
		if list.Items[i].Name == name {
			return &list.Items[i], nil
		}
	}
	return nil, apierrors.NewNotFound(node.Resource("etcdmembers"), name)
}

// Delete removes a member from the store: the control plane it ran on, gone
// or to be replaced, no longer counts for the store's majority.
func (s etcdMemberStorage) Delete(ctx context.Context, name string, _ rest.ValidateObjectFunc, _ *metav1.DeleteOptions) (runtime.Object, bool, error) {
	obj, err := s.Get(ctx, name, nil)
	if err != nil {
		return nil, false, err
	}
	m := obj.(*node.EtcdMember)
	id, err := strconv.ParseUint(m.Status.ID, 16, 64)
	if err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	c, err := etcdstore.Client()
	if err != nil {
		return nil, false, apierrors.NewInternalError(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if _, err := c.MemberRemove(ctx, id); err != nil {
		return nil, false, apierrors.NewInternalError(fmt.Errorf("remove member %s: %w", name, err))
	}
	return obj, true, nil
}

func (etcdMemberStorage) ConvertToTable(_ context.Context, obj runtime.Object, _ runtime.Object) (*metav1.Table, error) {
	var items []runtime.Object
	switch o := obj.(type) {
	case *node.EtcdMember:
		items = []runtime.Object{o}
	case *node.EtcdMemberList:
		for i := range o.Items {
			items = append(items, &o.Items[i])
		}
	}
	return table([]metav1.TableColumnDefinition{col("Name", "name"), col("ID", ""), col("Peer", ""), col("Learner", ""), col("Leader", "")},
		items, func(o runtime.Object) []any {
			m := o.(*node.EtcdMember)
			peer := ""
			if len(m.Status.PeerURLs) > 0 {
				peer = m.Status.PeerURLs[0]
			}
			return []any{m.Name, m.Status.ID, peer, m.Status.Learner, m.Status.Leader}
		}), nil
}

// promoteLearners gives a joined member its vote once it has caught up with
// the store; until then the store refuses, and the next pass tries again.
func promoteLearners(ctx context.Context) {
	for ctx.Err() == nil {
		func() {
			c, err := etcdstore.Client()
			if err != nil {
				return
			}
			defer c.Close()
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			resp, err := c.MemberList(pctx)
			if err != nil {
				return
			}
			for _, m := range resp.Members {
				if !m.IsLearner || m.Name == "" {
					continue
				}
				if _, err := c.MemberPromote(pctx, m.ID); err != nil {
					klog.V(2).Infof("cluster store: member %s not promoted yet: %v", m.Name, err)
					continue
				}
				klog.Infof("cluster store: member %s caught up and votes now", m.Name)
			}
		}()
		select {
		case <-ctx.Done():
		case <-time.After(15 * time.Second):
		}
	}
}

// controlPlaneShare reads what a joining control plane receives: the keys
// every control plane holds the same, from this node's PKI directory.
func controlPlaneShare(pkiDir string) (*nodev1.ControlPlaneShare, error) {
	read := func(name string) (string, error) {
		raw, err := os.ReadFile(filepath.Join(pkiDir, name))
		return string(raw), err
	}
	share := &nodev1.ControlPlaneShare{}
	for dst, name := range map[*string]string{
		&share.ClusterCAKey: "ca.key", &share.FrontProxyCAKey: "front-proxy-ca.key",
		&share.ServiceAccountKey: "sa.key", &share.ServiceAccountPub: "sa.pub",
		&share.EncryptionKey: "encryption.key",
		&share.EtcdCA:        etcdstore.CA + ".crt", &share.EtcdCAKey: etcdstore.CA + ".key",
	} {
		v, err := read(name)
		if err != nil {
			return nil, err
		}
		*dst = v
	}
	return share, nil
}
