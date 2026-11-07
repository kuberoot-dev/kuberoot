// Command kinit is PID 1 of every kuberoot distro.
//
// Stage one runs from the initramfs: it mounts the read-only root filesystem
// image and switches into it. Stage two runs from that root: it prepares the
// kernel interfaces a Kubernetes node needs and supervises the node services.
package main

import (
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/kuberoot-dev/kuberoot/pkg/distro"
)

const rootImage = "/rootfs.squashfs"

func main() {
	log.SetFlags(0)
	log.SetPrefix("[kinit] ")
	if os.Getpid() != 1 {
		log.Fatal("must run as PID 1")
	}
	if isInitramfs() {
		if err := switchRoot(); err != nil {
			log.Fatalf("switch root: %v", err)
		}
		return // unreachable: switchRoot execs stage two
	}
	if err := boot(); err != nil {
		log.Printf("boot failed: %v", err)
	}
	waitForSignals()
}

// isInitramfs tells stage one from stage two: only the initramfs has /newroot.
func isInitramfs() bool {
	_, err := os.Stat(newRoot)
	return err == nil
}

func boot() error {
	if err := mountAll(); err != nil {
		return err
	}
	var uts unix.Utsname
	_ = unix.Uname(&uts)
	log.Printf("kuberoot booting, kernel %s, uptime %s", unix.ByteSliceToString(uts.Release[:]), uptime())
	go watchCPUHotplug()

	waitForEntropy()
	cfg := loadCmdline()
	activeProfile = loadProfileOrRescue()
	cfg.podNetwork = podNetworkDefault(cfg.podNetwork, activeProfile)
	podNetworkMode = cfg.podNetwork
	loadModulesAndLock(activeProfile.Spec.Modules)
	if err := applySysctls(activeProfile.Spec.Sysctls); err != nil {
		log.Printf("sysctls: %v", err)
	}
	go watchPowerButton()
	go probationWatchdog()

	// Nothing below gives up: an edge node often comes up before its network,
	// and a node that stops trying can only be fixed by hand.
	var node nodeInfo
	retry("network", func() error {
		var err error
		node, err = setupNetwork(cfg)
		return err
	})
	retry("hostname", func() error { return setHostname(node) })

	lifecycle.Lock()
	defer lifecycle.Unlock()
	var services []service
	var controlPlane bool
	retry("node configuration", func() error {
		var err error
		services, controlPlane, err = configure(node, cfg)
		return err
	})
	if cfg.dev {
		printNodeKubeconfig()
	}
	serveControl(func() { reconfigure(node, cfg) })
	forgetResourceAssignments()
	startServices(services, cfg)
	if controlPlane {
		go applyAddons(generation(), cfg, node)
	}
	return nil
}

// retry runs step until it succeeds, backing off up to a minute between tries.
func retry(what string, step func() error) {
	backoff := 2 * time.Second
	for attempt := 1; ; attempt++ {
		err := step()
		if err == nil {
			return
		}
		log.Printf("%s failed (attempt %d), retrying in %s: %v", what, attempt, backoff, err)
		time.Sleep(backoff)
		backoff = min(backoff*2, time.Minute)
	}
}

// waitForEntropy says so on the console when the kernel random pool is not ready:
// key generation would block silently on hardware without an RNG source.
func waitForEntropy() {
	buf := make([]byte, 1)
	if _, err := unix.Getrandom(buf, unix.GRND_NONBLOCK); err != unix.EAGAIN {
		return
	}
	log.Printf("waiting for the kernel random pool (no hardware RNG?)")
	_, _ = unix.Getrandom(buf, 0)
	log.Printf("random pool ready, uptime %s", uptime())
}

// lifecycle serialises everything that starts or stops the node's services:
// boot, role switches and shutdown.
var (
	lifecycle    sync.Mutex
	shuttingDown bool
)

// waitForSignals reaps children and turns termination signals into a shutdown.
// The shutdown runs elsewhere: it waits for services to exit, and only this
// loop reaps them.
func waitForSignals() {
	sigs := make(chan os.Signal, 16)
	signal.Notify(sigs, unix.SIGCHLD, unix.SIGTERM, unix.SIGINT, unix.SIGPWR)
	for sig := range sigs {
		switch sig {
		case unix.SIGCHLD:
			reap()
		case unix.SIGINT:
			go shutdown(unix.LINUX_REBOOT_CMD_RESTART, "SIGINT")
		default:
			go shutdown(unix.LINUX_REBOOT_CMD_POWER_OFF, sig.String())
		}
	}
}

// shutdown stops the services, then every other process, unmounts the state
// partition cleanly and powers off or restarts. Only the first call acts.
func shutdown(cmd int, why string) {
	lifecycle.Lock()
	if shuttingDown {
		lifecycle.Unlock()
		return
	}
	shuttingDown = true
	log.Printf("%s: stopping services", why)
	stopServices()
	lifecycle.Unlock()

	// Container shims and anything else left: ask, wait, then insist.
	_ = unix.Kill(-1, unix.SIGTERM)
	time.Sleep(5 * time.Second)
	_ = unix.Kill(-1, unix.SIGKILL)
	time.Sleep(time.Second)
	unix.Sync()
	unmountState()
	unix.Sync()
	log.Printf("%s", map[int]string{unix.LINUX_REBOOT_CMD_RESTART: "restarting", unix.LINUX_REBOOT_CMD_POWER_OFF: "powering off"}[cmd])
	_ = unix.Reboot(cmd)
}

// unmountState leaves the ext4 state partition clean: everything mounted below
// /var goes first, then /var itself; if something still holds it, it is at
// least made read-only so the journal is consistent.
func unmountState() {
	raw, _ := os.ReadFile("/proc/self/mounts")
	var below []string
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		// /opt/cni/bin is a bind mount of a directory on /var.
		if len(f) > 1 && (strings.HasPrefix(f[1], "/var/") || f[1] == "/opt/cni/bin") {
			below = append(below, f[1])
		}
	}
	for i := len(below) - 1; i >= 0; i-- {
		_ = unix.Unmount(below[i], unix.MNT_DETACH)
	}
	if err := unix.Unmount("/var", 0); err != nil {
		_ = unix.Mount("", "/var", "", unix.MS_REMOUNT|unix.MS_RDONLY, "")
	}
}

func uptime() string {
	var info unix.Sysinfo_t
	if unix.Sysinfo(&info) != nil {
		return "?"
	}
	return (unixSeconds(info.Uptime)).String()
}

// podNetworkDefault settles how pod traffic crosses between nodes: the boot
// argument first, then what the distribution declares, and vxlan when neither
// says. A distribution that installs a CNI declares "none", so the node does
// not raise a tunnel of its own on the port the CNI needs.
func podNetworkDefault(fromCmdline string, profile *distro.Profile) string {
	if fromCmdline != "" {
		return fromCmdline
	}
	if profile != nil {
		switch profile.Spec.PodNetwork {
		case "vxlan", "host-gw", "none":
			return profile.Spec.PodNetwork
		}
	}
	return "vxlan"
}
