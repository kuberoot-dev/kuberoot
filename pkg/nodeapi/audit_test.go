package nodeapi

import (
	"bytes"
	"encoding/json"
	authentication "k8s.io/api/authentication/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	auditapi "k8s.io/apiserver/pkg/apis/audit"
	"k8s.io/apiserver/pkg/authorization/authorizer"
	genericapiserver "k8s.io/apiserver/pkg/server"
	"testing"
)

func TestNodeAuditRecordsMutationsWithoutBodies(t *testing.T) {
	var out bytes.Buffer
	cfg := genericapiserver.NewRecommendedConfig(codecs)
	configureNodeAudit(cfg, &out)
	for _, verb := range []string{"create", "update", "patch", "delete", "deletecollection"} {
		a := authorizer.AttributesRecord{Verb: verb, ResourceRequest: true, APIGroup: "node.kuberoot.dev", Resource: "upgrades", Name: "release-b"}
		rule := cfg.AuditPolicyRuleEvaluator.EvaluatePolicyRule(a)
		if rule.Level != auditapi.LevelMetadata {
			t.Fatalf("%s level=%s", verb, rule.Level)
		}
		if len(rule.OmitStages) != 1 || rule.OmitStages[0] != auditapi.StageRequestReceived {
			t.Fatalf("duplicate/missing audit stages: %v", rule.OmitStages)
		}
	}
	if rule := cfg.AuditPolicyRuleEvaluator.EvaluatePolicyRule(authorizer.AttributesRecord{Verb: "get", ResourceRequest: true, Resource: "kubeconfigs"}); rule.Level != auditapi.LevelNone {
		t.Fatal("credential reads should not log bodies or generate polling noise")
	}
	event := &auditapi.Event{Level: auditapi.LevelMetadata, Stage: auditapi.StageResponseComplete, Verb: "create", User: authentication.UserInfo{Username: "operator"}, ObjectRef: &auditapi.ObjectReference{Resource: "upgrades", Name: "release-b", APIGroup: "node.kuberoot.dev"}, ResponseStatus: &metav1.Status{Code: 201}}
	if !cfg.AuditBackend.ProcessEvents(event) {
		t.Fatal("audit event lost")
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["user"].(map[string]any)["username"] != "operator" || got["objectRef"].(map[string]any)["name"] != "release-b" || got["responseStatus"].(map[string]any)["code"] != float64(201) {
		t.Fatalf("missing audit identity/outcome: %s", out.Bytes())
	}
}
