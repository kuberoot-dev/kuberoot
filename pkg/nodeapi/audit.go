package nodeapi

import (
	"io"
	auditapi "k8s.io/apiserver/pkg/apis/audit"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
	"k8s.io/apiserver/pkg/audit/policy"
	genericapiserver "k8s.io/apiserver/pkg/server"
	auditlog "k8s.io/apiserver/plugin/pkg/audit/log"
)

// The standalone node API must record changes too: those requests need not
// traverse the cluster API server. Stderr is retained by the supervisor and
// available through nodeservices/log, including without a working cluster API.
// Metadata includes actor, object and outcome but never credentials or bodies.
func configureNodeAudit(cfg *genericapiserver.RecommendedConfig, out io.Writer) {
	cfg.AuditBackend = auditlog.NewBackend(out, auditlog.FormatJson, auditv1.SchemeGroupVersion)
	cfg.AuditPolicyRuleEvaluator = policy.NewPolicyRuleEvaluator(&auditapi.Policy{
		OmitStages: []auditapi.Stage{auditapi.StageRequestReceived},
		Rules: []auditapi.PolicyRule{
			{Level: auditapi.LevelMetadata, Verbs: []string{"create", "update", "patch", "delete", "deletecollection"}},
			{Level: auditapi.LevelNone},
		},
	})
}
