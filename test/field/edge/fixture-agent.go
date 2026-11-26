// fixture-agent is an intentionally faulty service baked into test-only releases.
// It must never be included in production images. It runs inside the DUT VM.
package main

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

func nft(rules string) {
	cmd := exec.Command("nft", "-f", "-")
	cmd.Stdin = bytes.NewBufferString(rules)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Fatalf("nft: %v: %s", err, out)
	}
}

func main() {
	if len(os.Args) != 2 {
		log.Fatal("fixture-agent init-crash|pod-network|wrong-content|incompatible-state")
	}
	cmdline, err := os.ReadFile("/proc/cmdline")
	serial, _ := os.ReadFile("/sys/class/dmi/id/product_serial")
	if err != nil || (!strings.Contains(string(cmdline), "kuberoot.root=PARTLABEL=kuberoot-root-") && strings.TrimSpace(string(serial)) != "field-edge-fault-test") {
		log.Fatal("refusing to inject outside an explicitly marked field-test guest")
	}
	log.Printf("TEST-ONLY fault service: %s", os.Args[1])
	switch os.Args[1] {
	case "init-crash":
		log.Fatal("intentional init-service crash")
	case "pod-network":
		nft(`table inet field_fault {
   chain forward {
    type filter hook forward priority -200; policy accept;
    tcp dport { 53, 8080 } counter drop
    udp dport 53 counter drop
   }
   chain output {
    type filter hook output priority 0; policy accept;
    tcp dport { 53, 8080 } counter drop
    udp dport 53 counter drop
   }
  }`)
		log.Print("injected pod-network drop rules")
		for {
			time.Sleep(time.Hour)
		}
	case "wrong-content":
		// Return incorrect application bytes while the real pods and API stay Ready.
		// This volatile NAT rule disappears when the OS rolls back and reboots.
		nft(`table inet field_fault {
   chain prerouting {
    type nat hook prerouting priority -110; policy accept;
    tcp dport 30080 counter redirect to :18080
   }
   chain output {
    type nat hook output priority -110; policy accept;
    tcp dport 30080 counter redirect to :18080
   }
  }`)
		log.Print("injected wrong-content NAT rule")
		http.HandleFunc("/http", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = w.Write(bytes.Repeat([]byte("X"), 1024))
		})
		log.Fatal(http.ListenAndServe(":18080", nil))
	case "incompatible-state":
		// Model a format migration that the old image cannot interpret. Never touch
		// any file except this test workload's dedicated guest-local record file.
		const path = "/var/lib/field-records/records"
		for {
			st, err := os.Stat(path)
			if err == nil && st.Size() >= 1024 {
				break
			}
			time.Sleep(time.Second)
		}
		f, err := os.OpenFile(path, os.O_RDWR, 0)
		if err != nil {
			log.Fatal(err)
		}
		header := append([]byte("FIELD-STATE-V2-INCOMPATIBLE\n"), bytes.Repeat([]byte("V"), 1024)...)
		if _, err := f.WriteAt(header[:1024], 0); err != nil {
			log.Fatal(err)
		}
		if err := f.Sync(); err != nil {
			log.Fatal(err)
		}
		if err := f.Close(); err != nil {
			log.Fatal(err)
		}
		log.Print("test state migrated incompatibly; original record 0 overwritten")
		for {
			time.Sleep(time.Hour)
		}
	default:
		log.Fatal(fmt.Sprintf("unknown test fault %q", os.Args[1]))
	}
}
