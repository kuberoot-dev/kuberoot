package nodeapi

import (
	"context"
	"testing"

	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"
)

func TestAggregatorMayReadOnlyTheOpenAPIDocument(t *testing.T) {
	aggregator := &user.DefaultInfo{Name: "system:aggregator", Groups: []string{viaClusterGroup}}
	for _, c := range []struct {
		name  string
		attrs authorizer.AttributesRecord
		want  authorizer.Decision
	}{
		{"openapi v2", authorizer.AttributesRecord{User: aggregator, Verb: "get", Path: "/openapi/v2"}, authorizer.DecisionAllow},
		{"openapi v3 group", authorizer.AttributesRecord{User: aggregator, Verb: "get", Path: "/openapi/v3/apis/node.kuberoot.dev/v1alpha1"}, authorizer.DecisionAllow},
		{"other path", authorizer.AttributesRecord{User: aggregator, Verb: "get", Path: "/metrics"}, authorizer.DecisionNoOpinion},
		{"a resource", authorizer.AttributesRecord{User: aggregator, Verb: "get", ResourceRequest: true, APIGroup: "node.kuberoot.dev", Resource: "osconfigs"}, authorizer.DecisionNoOpinion},
		{"not through the front proxy", authorizer.AttributesRecord{User: &user.DefaultInfo{Name: "system:aggregator"}, Verb: "get", Path: "/openapi/v2"}, authorizer.DecisionNoOpinion},
		{"another user", authorizer.AttributesRecord{User: &user.DefaultInfo{Name: "alice", Groups: []string{viaClusterGroup}}, Verb: "get", Path: "/openapi/v2"}, authorizer.DecisionNoOpinion},
	} {
		if got, _, _ := authorize(context.Background(), c.attrs); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
