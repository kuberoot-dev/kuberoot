package node

import (
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const GroupName = "node.kuberoot.dev"

var SchemeGroupVersion = schema.GroupVersion{Group: GroupName, Version: runtime.APIVersionInternal}

var (
	SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)
	AddToScheme   = SchemeBuilder.AddToScheme
)

func Resource(resource string) schema.GroupResource {
	return SchemeGroupVersion.WithResource(resource).GroupResource()
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
		&EtcdRotation{}, &EtcdRotationList{},
		&EtcdCertificate{}, &EtcdCertificateList{},
		&LatencyTest{}, &LatencyTestList{},
		&FleetEnrollment{}, &FleetEnrollmentList{},
	)
	return nil
}
