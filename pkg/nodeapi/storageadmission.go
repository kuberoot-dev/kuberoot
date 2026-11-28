package nodeapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	admissionv1 "k8s.io/api/admission/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apiserver/pkg/authentication/authenticator"
	"k8s.io/apiserver/pkg/authentication/user"
	"k8s.io/apiserver/pkg/authorization/authorizer"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

// StorageAdmissionPath is where the cluster API server asks, through a
// validating webhook, whether a change can be stored. On a node whose
// cluster store is kine on a state partition the kernel made read-only, or
// whose device no longer answers, kine does not fail a write: it waits, and
// the API server times the request out with no word about why. The webhook
// refuses the change first, saying why.
const StorageAdmissionPath = "/admission/state-storage"

// kineSocket is there when this node's cluster store is kine.
const kineSocket = "/run/kine/kine.sock"

// storageRefusal is why a change cannot be stored now, or "".
func storageRefusal(st *node.StateStorage, kineHere bool) string {
	if st == nil || !kineHere {
		return ""
	}
	switch st.State {
	case StorageReadOnly, StorageFailing:
		return fmt.Sprintf("the node's state partition %s is %s (%s): the cluster store cannot keep changes until the node is repaired", st.Device, st.State, st.Message)
	}
	return ""
}

func serveStorageAdmission(w http.ResponseWriter, r *http.Request) {
	var review admissionv1.AdmissionReview
	if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&review) != nil || review.Request == nil {
		http.Error(w, "an AdmissionReview is expected", http.StatusBadRequest)
		return
	}
	_, err := os.Stat(kineSocket)
	resp := &admissionv1.AdmissionResponse{UID: review.Request.UID, Allowed: true}
	if why := storageRefusal(readStateStorage(), err == nil); why != "" {
		resp.Allowed = false
		resp.Result = &metav1.Status{Status: metav1.StatusFailure, Code: http.StatusServiceUnavailable, Reason: metav1.StatusReasonServiceUnavailable, Message: why}
	}
	review.Response, review.Request = resp, nil
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(review)
}

// admissionCaller lets the API server's webhook call in: it presents no
// client certificate, and the answer it gets says only whether the node can
// store changes.
var admissionCaller = authenticator.RequestFunc(func(r *http.Request) (*authenticator.Response, bool, error) {
	if r.URL.Path != StorageAdmissionPath {
		return nil, false, nil
	}
	return &authenticator.Response{User: &user.DefaultInfo{Name: user.Anonymous, Groups: []string{user.AllUnauthenticated}}}, true, nil
})

func admissionAllowed(a authorizer.Attributes) bool {
	return !a.IsResourceRequest() && a.GetPath() == StorageAdmissionPath && a.GetVerb() == "post"
}
