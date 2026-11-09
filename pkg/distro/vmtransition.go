package distro

import "os"

// VMAPITransitionPath is written only by an explicitly selected transition
// image build. Ordinary alpha2 images cannot start the legacy controller.
const VMAPITransitionPath = "/usr/share/kuberoot/vm-api-transition"

const VMAPITransitionMarker = "v1alpha1-to-v1alpha2\n"

func VMAPITransitionEnabled() bool {
	raw, err := os.ReadFile(VMAPITransitionPath)
	return err == nil && string(raw) == VMAPITransitionMarker
}
