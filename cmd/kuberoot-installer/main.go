// Command kuberoot-installer is the console installer of the live system. It is
// a client of the node API like kubectl: everything it does can be done remotely.
package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	nodev1 "github.com/kuberoot-dev/kuberoot/pkg/apis/node/v1alpha1"
)

const (
	api    = "https://127.0.0.1:50000/apis/node.kuberoot.dev/v1alpha1"
	pkiDir = "/var/lib/kuberoot/pki/"
)

var (
	title   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15")).Background(lipgloss.Color("25")).Padding(0, 2)
	box     = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("25")).Padding(1, 2).Width(72)
	dim     = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	pick    = lipgloss.NewStyle().Foreground(lipgloss.Color("15")).Background(lipgloss.Color("25"))
	warn    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("203"))
	success = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("78"))
)

type screen int

const (
	welcome screen = iota
	choose
	confirm
	installing
	done
	failed
)

type model struct {
	client  *http.Client
	screen  screen
	osc     nodev1.OSConfig
	disks   []nodev1.Disk
	cursor  int
	install nodev1.Installation
	err     error
}

type loaded struct {
	osc   nodev1.OSConfig
	disks []nodev1.Disk
	err   error
}
type polled struct {
	inst nodev1.Installation
	err  error
}
type tick struct{}

func main() {
	client, err := nodeClient()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m := model{client: client}
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func nodeClient() (*http.Client, error) {
	cert, err := tls.LoadX509KeyPair(pkiDir+"node-admin.crt", pkiDir+"node-admin.key")
	if err != nil {
		return nil, err
	}
	ca, err := os.ReadFile(pkiDir + "node-serving-ca.crt")
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(ca)
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{
		TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, RootCAs: pool},
	}}, nil
}

func (m model) Init() tea.Cmd { return m.load }

func (m model) load() tea.Msg {
	var osl nodev1.OSConfigList
	var dl nodev1.DiskList
	for {
		err := m.get("/osconfigs", &osl)
		if err == nil {
			err = m.get("/disks", &dl)
		}
		if err == nil && len(osl.Items) > 0 {
			var free []nodev1.Disk
			for _, d := range dl.Items {
				if d.Status.Role != "BootMedia" {
					free = append(free, d)
				}
			}
			return loaded{osc: osl.Items[0], disks: free}
		}
		time.Sleep(time.Second) // the node API may still be starting
	}
}

func (m model) get(path string, out any) error {
	resp, err := m.client.Get(api + path)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (m model) start() tea.Msg {
	inst := nodev1.Installation{
		TypeMeta: metaType("Installation"),
		Spec:     nodev1.InstallationSpec{Disk: m.disks[m.cursor].Name, Reboot: true},
	}
	inst.Name = "console"
	body, _ := json.Marshal(inst)
	resp, err := m.client.Post(api+"/installations", "application/json", bytes.NewReader(body))
	if err != nil {
		return polled{err: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var status struct{ Message string }
		_ = json.NewDecoder(resp.Body).Decode(&status)
		return polled{err: fmt.Errorf("%s", status.Message)}
	}
	return tick{}
}

func (m model) poll() tea.Msg {
	var inst nodev1.Installation
	err := m.get("/installations/console", &inst)
	return polled{inst: inst, err: err}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case loaded:
		m.osc, m.disks = msg.osc, msg.disks
	case tick:
		return m, m.poll
	case polled:
		if msg.err != nil {
			m.screen, m.err = failed, msg.err
			return m, nil
		}
		m.install = msg.inst
		switch msg.inst.Status.Phase {
		case "Completed":
			m.screen = done
			return m, nil
		case "Failed":
			m.screen, m.err = failed, fmt.Errorf("%s", msg.inst.Status.Message)
			return m, nil
		}
		return m, tea.Tick(500*time.Millisecond, func(time.Time) tea.Msg { return tick{} })
	case tea.KeyMsg:
		return m.key(msg.String())
	}
	return m, nil
}

func (m model) key(k string) (tea.Model, tea.Cmd) {
	switch m.screen {
	case welcome:
		if k == "enter" && len(m.disks) > 0 {
			m.screen = choose
		}
	case choose:
		switch k {
		case "up", "k":
			m.cursor = max(0, m.cursor-1)
		case "down", "j":
			m.cursor = min(len(m.disks)-1, m.cursor+1)
		case "enter":
			m.screen = confirm
		case "esc":
			m.screen = welcome
		}
	case confirm:
		switch k {
		case "y", "Y":
			m.screen = installing
			return m, m.start
		case "n", "N", "esc":
			m.screen = choose
		}
	case failed:
		if k == "enter" {
			m.screen, m.err = choose, nil
		}
	}
	return m, nil
}

func (m model) View() string {
	head := title.Render("kuberoot " + m.osc.Status.Version + " installer")
	var body string
	switch m.screen {
	case welcome:
		if m.osc.Name == "" {
			body = "Starting the node API..."
			break
		}
		body = fmt.Sprintf("This machine is running kuberoot from the boot media.\n\n"+
			"  node       %s\n  address    %s\n  kernel     %s\n\n"+
			"The same installation can be done remotely with kubectl:\n"+
			"%s\n\n%s",
			m.osc.Name, strings.Join(m.osc.Status.Addresses, ", "), m.osc.Status.KernelVersion,
			dim.Render(fmt.Sprintf("  node API   https://%s:50000\n  kubectl get disks\n  kubectl create -f installation.yaml", firstOr(m.osc.Status.Addresses, "<address>"))),
			disksOrNone(m.disks))
	case choose:
		var b strings.Builder
		b.WriteString("Choose the disk to install on. Everything on it will be erased.\n\n")
		for i, d := range m.disks {
			line := fmt.Sprintf(" %-8s %8s  %-20s %s ", d.Name, human(d.Status.SizeBytes), d.Status.Model, d.Status.Role)
			if i == m.cursor {
				line = pick.Render(line)
			}
			b.WriteString(line + "\n")
		}
		b.WriteString(dim.Render("\n↑/↓ select · enter continue · esc back"))
		body = b.String()
	case confirm:
		d := m.disks[m.cursor]
		body = warn.Render(fmt.Sprintf("All data on %s (%s) will be erased.", d.Name, human(d.Status.SizeBytes))) +
			"\n\nkuberoot will create the boot partition, two root slots for updates\nand rollback, and a state partition for everything the node keeps.\n\n" +
			dim.Render("y install · n back")
	case installing:
		st := m.install.Status
		body = fmt.Sprintf("Installing on %s\n\n%s %3d%%\n\n%s", m.disks[m.cursor].Name, bar(int(st.Progress), 50), st.Progress, dim.Render(st.Message))
	case done:
		body = success.Render("kuberoot is installed.") + "\n\nRemove the boot media. The machine is rebooting into the installed system."
	case failed:
		body = warn.Render("Installation failed") + "\n\n" + fmt.Sprint(m.err) + "\n\n" + dim.Render("enter back")
	}
	return "\n" + head + "\n\n" + box.Render(body) + "\n"
}

func bar(percent, width int) string {
	filled := width * percent / 100
	return lipgloss.NewStyle().Foreground(lipgloss.Color("25")).Render(strings.Repeat("█", filled)) + dim.Render(strings.Repeat("░", width-filled))
}

func human(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(b)/(1<<30))
	default:
		return fmt.Sprintf("%d MiB", b>>20)
	}
}

func firstOr(list []string, fallback string) string {
	if len(list) > 0 {
		return list[0]
	}
	return fallback
}

func disksOrNone(disks []nodev1.Disk) string {
	if len(disks) == 0 {
		return warn.Render("No disk to install on was found.")
	}
	return dim.Render("enter continue")
}
