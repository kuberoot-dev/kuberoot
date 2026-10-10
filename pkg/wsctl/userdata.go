// Package wsctl runs a workstation cluster's workspaces: each a virtual
// machine with a desktop that cloud-init installs at its first boot, and a
// gateway on the control plane that opens it in a browser (noVNC over a
// WebSocket) or for an RDP client, for whoever holds its token.
package wsctl

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"math/big"
	"text/template"
)

// VNCPort and RDPPort are where the desktop listens in the machine.
const (
	VNCPort = 5901
	RDPPort = 3389
)

// desktop is the machine's cloud-init user data: the owner's account, a
// light desktop (Xfce), a VNC server that keeps the session running between
// connections, and xrdp for RDP clients. Recommends are left out: no display
// manager, no extra services. Every browser reaches the VNC server from the
// gateway's one address, so its blacklist, which locks an address out after
// a few unauthenticated connections, would lock everyone out: the gateway's
// token is what keeps strangers away.
var desktop = template.Must(template.New("desktop").Parse(`#cloud-config
hostname: {{.Hostname}}
users:
  - default
  - name: {{.Owner}}
    shell: /bin/bash
    groups: [sudo]
    sudo: ALL=(ALL) NOPASSWD:ALL
    lock_passwd: false
chpasswd:
  expire: false
  users:
    - {name: {{.Owner}}, password: {{.Password}}, type: text}
ssh_pwauth: false
apt:
  conf: |
    APT::Install-Recommends "false";
package_update: true
packages:
  - xfce4
  - xfce4-terminal
  - dbus-x11
  - tigervnc-standalone-server
  - tigervnc-tools
  - xrdp
  - xorgxrdp
write_files:
  - path: /etc/systemd/system/kuberoot-desktop.service
    content: |
      [Unit]
      Description=The workspace's desktop, kept running between connections
      After=network-online.target
      [Service]
      User={{.Owner}}
      WorkingDirectory=/home/{{.Owner}}
      Environment=HOME=/home/{{.Owner}}
      ExecStartPre=/bin/sh -c 'mkdir -p $HOME/.vnc && echo "$PASS" | tigervncpasswd -f > $HOME/.vnc/passwd && chmod 600 $HOME/.vnc/passwd'
      ExecStart=/usr/bin/tigervncserver :1 -fg -localhost no -geometry 1600x900 -SecurityTypes VncAuth -BlacklistThreshold 1000000 -xstartup /usr/bin/startxfce4
      Environment=PASS={{.Password}}
      Restart=always
      [Install]
      WantedBy=multi-user.target
  - path: /etc/skel/.xsession
    content: |
      startxfce4
runcmd:
  - cp /etc/skel/.xsession /home/{{.Owner}}/.xsession
  - chown {{.Owner}}:{{.Owner}} /home/{{.Owner}}/.xsession
  - systemctl daemon-reload
  - systemctl enable --now kuberoot-desktop.service
  - systemctl enable --now xrdp
`))

// UserData renders the desktop's cloud-init user data.
func UserData(hostname, owner, password string) (string, error) {
	var b bytes.Buffer
	err := desktop.Execute(&b, struct{ Hostname, Owner, Password string }{hostname, owner, password})
	return b.String(), err
}

// Token is a workspace's secret in its URL.
func Token() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Password is the owner's password, letters and digits only: it goes
// unquoted into YAML and a shell, and is typed by a person. VNC reads only
// its first eight characters.
func Password() (string, error) {
	const alphabet = "abcdefghjkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	out := make([]byte, 12)
	for i := range out {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		if err != nil {
			return "", err
		}
		out[i] = alphabet[n.Int64()]
	}
	return string(out), nil
}
