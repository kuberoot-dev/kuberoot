package nodeapi

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"

	"github.com/kuberoot-dev/kuberoot/pkg/apis/node"
)

func TestStorageRefusal(t *testing.T) {
	failing := &node.StateStorage{State: StorageFailing, Device: "/dev/vdb1", Message: "read /dev/vdb1: input/output error"}
	for _, c := range []struct {
		name string
		st   *node.StateStorage
		kine bool
		want bool
	}{
		{"failing, kine here", failing, true, true},
		{"read-only, kine here", &node.StateStorage{State: StorageReadOnly}, true, true},
		{"healthy", &node.StateStorage{State: StorageHealthy}, true, false},
		{"full: writes may still fit", &node.StateStorage{State: StorageFull}, true, false},
		{"failing, the store is elsewhere", failing, false, false},
		{"no state partition", nil, true, false},
	} {
		if got := storageRefusal(c.st, c.kine) != ""; got != c.want {
			t.Errorf("%s: refused %v, want %v", c.name, got, c.want)
		}
	}
	if why := storageRefusal(failing, true); !strings.Contains(why, "input/output error") {
		t.Errorf("the refusal does not say why: %q", why)
	}
}

func TestStorageAdmissionAnswersTheReview(t *testing.T) {
	body, _ := json.Marshal(admissionv1.AdmissionReview{Request: &admissionv1.AdmissionRequest{UID: "42"}})
	rec := httptest.NewRecorder()
	serveStorageAdmission(rec, httptest.NewRequest(http.MethodPost, StorageAdmissionPath, bytes.NewReader(body)))
	var got admissionv1.AdmissionReview
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Response == nil || got.Response.UID != "42" {
		t.Fatalf("response %s: %v", rec.Body.String(), err)
	}
	rec = httptest.NewRecorder()
	serveStorageAdmission(rec, httptest.NewRequest(http.MethodGet, StorageAdmissionPath, nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("GET: %d", rec.Code)
	}
}
