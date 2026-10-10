package vm

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/tym83/kuberoot/pkg/apis/node"
)

func TestResConfig(t *testing.T) {
	v := &Volumes{Node: "node-b", Address: "10.0.0.2"}
	s := node.VolumeSpec{SizeBytes: 1 << 30, Minor: 100, Port: 7800, NodeID: 1,
		Peers: []node.VolumePeer{{Node: "node-a", Address: "10.0.0.1", NodeID: 0}, {Node: "node-c", Address: "10.0.0.3", NodeID: 2}}}
	got := v.ResConfig("web", s, "/dev/loop3")
	for _, want := range []string{
		"resource vm-web {",
		"quorum majority;",
		"on-no-quorum io-error;",
		"after-sb-1pri discard-secondary;",
		"device minor 100;\n\t\tdisk /dev/loop3;",
		"on node-a {\n\t\tnode-id 0;\n\t\taddress 10.0.0.1:7800;",
		"on node-b {\n\t\tnode-id 1;\n\t\taddress 10.0.0.2:7800;",
		"hosts node-a node-b node-c;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("resource lacks %q:\n%s", want, got)
		}
	}
	if single := v.ResConfig("solo", node.VolumeSpec{Minor: 101, Port: 7801}, "/dev/loop4"); strings.Contains(single, "connection-mesh") {
		t.Errorf("a volume without peers has a mesh:\n%s", single)
	}
}

func TestParseStatus(t *testing.T) {
	raw := `[{"name":"vm-web","node-id":1,"role":"Primary","suspended":false,
		"devices":[{"volume":0,"minor":100,"disk-state":"UpToDate","client":false,"quorum":true}],
		"connections":[{"peer-node-id":0,"name":"node-a","connection-state":"Connected"},
		               {"peer-node-id":2,"name":"node-c","connection-state":"Connecting"}]}]`
	st, err := ParseStatus([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if st.Role != "Primary" || st.DiskState != "UpToDate" || !st.Quorum || st.Device != "/dev/drbd100" ||
		st.PeerStates["node-a"] != "Connected" || st.PeerStates["node-c"] != "Connecting" || st.Phase != "Ready" {
		t.Errorf("status = %+v", st)
	}
}

func TestMachineArgs(t *testing.T) {
	m := &Machines{}
	got := strings.Join(m.Args("web", node.MachineSpec{CPUs: 2, MemoryMiB: 1024, MAC: "52:54:00:aa:bb:cc"}, []string{"/dev/drbd100"}), " ")
	for _, want := range []string{"--cpus boot=2", "--memory size=1024M", "--firmware /usr/share/kuberoot/CLOUDHV.fd",
		"--disk path=/dev/drbd100", "--net tap=" + TapName("web") + ",mac=52:54:00:aa:bb:cc", "--api-socket path=/run/kuberoot/vm/web.sock"} {
		if !strings.Contains(got, want) {
			t.Errorf("args lack %q: %s", want, got)
		}
	}
	if len(TapName("a-very-long-machine-name-indeed")) > 15 {
		t.Error("tap name longer than a link name may be")
	}
}

func TestTwoPrimariesOnlyWhileMoving(t *testing.T) {
	v := &Volumes{Node: "a", Address: "10.0.0.1"}
	s := node.VolumeSpec{Minor: 100, Port: 7800, Peers: []node.VolumePeer{{Node: "b", Address: "10.0.0.2", NodeID: 1}}}
	if strings.Contains(v.ResConfig("web", s, "/dev/loop0"), "allow-two-primaries") {
		t.Error("two primaries allowed outside a move")
	}
	s.AllowTwoPrimaries = true
	if !strings.Contains(v.ResConfig("web", s, "/dev/loop0"), "allow-two-primaries yes;") {
		t.Error("two primaries not allowed during a move")
	}
}

func TestStatusReceiveFailed(t *testing.T) {
	RunDir = t.TempDir()
	m := &Machines{}
	s := node.MachineSpec{Running: true, Receive: "tcp:0.0.0.0:9001"}
	if err := os.WriteFile(m.mark("demo", "failed"), []byte("Error: connection refused\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := m.Status("demo", s); st.Phase != "Failed" || st.Message != "receiving: Error: connection refused" {
		t.Fatalf("got %+v", st)
	}
	s.Receive = ""
	if st := m.Status("demo", s); st.Phase != "Starting" {
		t.Fatalf("an ordinary start reported %+v", st)
	}
}

func TestVolumeLeavesTheNodeRoom(t *testing.T) {
	StateDir, RunDir = t.TempDir(), t.TempDir()
	v := &Volumes{Node: "n", Address: "10.0.0.1"}
	err := v.Apply(context.Background(), "huge", node.VolumeSpec{SizeBytes: 1 << 60, Minor: 100, Port: 7800})
	if err == nil || !strings.Contains(err.Error(), "no room for the volume") {
		t.Fatalf("a volume larger than the disk was made: %v", err)
	}
	if _, err := os.Stat(v.imageFile("huge")); err == nil {
		t.Fatal("its file was left")
	}
}

func TestArgsSystemSerial(t *testing.T) {
	m := &Machines{}
	args := strings.Join(m.Args("desk", node.MachineSpec{CPUs: 1, MemoryMiB: 512, SystemSerial: "ds=nocloud;s=http://g/desk/"}, nil), " ")
	if !strings.Contains(args, "--platform system_serial_number=ds=nocloud;s=http://g/desk/") {
		t.Fatalf("args: %s", args)
	}
	if strings.Contains(strings.Join(m.Args("plain", node.MachineSpec{CPUs: 1, MemoryMiB: 512}, nil), " "), "--platform") {
		t.Fatal("a platform serial for a machine with none")
	}
}
