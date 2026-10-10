// Command kuberoot-store serves the resource store prototype on the etcd v3
// API, for kube-apiserver to run on: a single member, no replication. See
// docs/resource-store.md.
package main

import (
	"context"
	"flag"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"k8s.io/klog/v2"

	"github.com/tym83/kuberoot/pkg/resourcestore"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:2379", "address of the etcd v3 API")
	data := flag.String("data", "/var/lib/kuberoot-store/store.db", "the store's file")
	klog.InitFlags(nil)
	flag.Parse()

	st, err := resourcestore.Open(*data)
	if err != nil {
		klog.Fatalf("open %s: %v", *data, err)
	}
	defer st.Close()
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		klog.Fatal(err)
	}
	// -v=4 logs every call and how it ended, for finding what a client
	// asked that the store does not answer as etcd would.
	g := grpc.NewServer(grpc.ChainUnaryInterceptor(func(ctx context.Context, req any, info *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		resp, err := h(ctx, req)
		klog.V(4).Infof("%s %v -> %v err=%v", info.FullMethod, req, resp, err)
		return resp, err
	}), grpc.ChainStreamInterceptor(func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		err := h(srv, ss)
		klog.V(4).Infof("%s stream ended: %v", info.FullMethod, err)
		return err
	}))
	(&resourcestore.Server{Store: st, ClusterID: 1, MemberID: 1}).Register(g)
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		<-ch
		g.GracefulStop()
	}()
	klog.Infof("resource store on %s, data in %s", *listen, *data)
	if err := g.Serve(l); err != nil {
		klog.Fatal(err)
	}
}
