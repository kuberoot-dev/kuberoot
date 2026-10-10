package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const GroupName = "node.kuberoot.dev"

var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: "v1alpha1"}

var (
	SchemeBuilder      runtime.SchemeBuilder
	localSchemeBuilder = &SchemeBuilder
	AddToScheme        = localSchemeBuilder.AddToScheme
)

func init() {
	localSchemeBuilder.Register(addKnownTypes)
}

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(SchemeGroupVersion,
		&OSConfig{}, &OSConfigList{},
		&NodeService{}, &NodeServiceList{},
		&Kubeconfig{}, &KubeconfigList{},
		&Disk{}, &DiskList{},
		&Installation{}, &InstallationList{},
		&BootEntry{}, &BootEntryList{},
		&Upgrade{}, &UpgradeList{},
		&Membership{}, &MembershipList{},
		&JoinTicket{}, &JoinTicketList{},
		&StateBackup{}, &StateBackupList{},
		&Volume{}, &VolumeList{},
		&Machine{}, &MachineList{},
		&ModelServer{}, &ModelServerList{},
		&EtcdMember{}, &EtcdMemberList{},
		&LatencyTest{}, &LatencyTestList{},
	)
	metav1.AddToGroupVersion(scheme, SchemeGroupVersion)
	return nil
}
