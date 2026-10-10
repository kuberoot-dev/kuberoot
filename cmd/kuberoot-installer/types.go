package main

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	nodev1 "github.com/kuberoot-dev/kuberoot/pkg/apis/node/v1alpha1"
)

func metaType(kind string) metav1.TypeMeta {
	return metav1.TypeMeta{APIVersion: nodev1.SchemeGroupVersion.String(), Kind: kind}
}
