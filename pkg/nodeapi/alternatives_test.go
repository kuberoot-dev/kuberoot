package nodeapi

import (
	"testing"

	"github.com/kuberoot-dev/kuberoot/pkg/supervisor"
)

func TestAlternativeLabelsPatch(t *testing.T) {
	statuses := []supervisor.ServiceStatus{{Name: "kube-apiserver", Alternative: "b"}, {Name: "kubelet"}}
	for _, c := range []struct {
		current map[string]string
		want    string
	}{
		{nil, `{"metadata":{"labels":{"alternative.kuberoot.dev/kube-apiserver":"b"}}}`},
		{map[string]string{"alternative.kuberoot.dev/kube-apiserver": "a"}, `{"metadata":{"labels":{"alternative.kuberoot.dev/kube-apiserver":"b"}}}`},
		{map[string]string{"alternative.kuberoot.dev/kube-apiserver": "b", "other": "x"}, ""},
		{map[string]string{"alternative.kuberoot.dev/kube-apiserver": "b", "alternative.kuberoot.dev/gone": "a"},
			`{"metadata":{"labels":{"alternative.kuberoot.dev/gone":null}}}`},
	} {
		if got := string(alternativeLabelsPatch(c.current, statuses)); got != c.want {
			t.Errorf("labels %v: patch %s, want %s", c.current, got, c.want)
		}
	}
}
