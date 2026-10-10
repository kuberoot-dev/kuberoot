// Command mkimage builds kuberoot boot media: a disk image to write to a USB
// stick, which boots the live system with the installer.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	diskfs "github.com/diskfs/go-diskfs"
	"github.com/diskfs/go-diskfs/disk"
	"github.com/diskfs/go-diskfs/filesystem"

	"github.com/kuberoot-dev/kuberoot/pkg/bootdisk"
)

func main() {
	var a bootdisk.Artifacts
	var out string
	var dev bool
	var extra, trustDir string
	flag.StringVar(&a.Dir, "artifacts", "", "directory with vmlinuz.efi, initrd.cpio, rootfs.squashfs, systemd-boot.efi")
	flag.StringVar(&a.Arch, "arch", "arm64", "target architecture")
	flag.StringVar(&a.Version, "version", "0.1.0-dev", "release version")
	flag.StringVar(&out, "out", "", "image file to create")
	flag.BoolVar(&dev, "dev", false, "development media: print node credentials on the console")
	flag.StringVar(&trustDir, "trust", "", "directory with the CAs that give access to nodes installed from this media; created if missing")
	flag.StringVar(&extra, "args", "", "extra kernel arguments, e.g. kuberoot.ip=eth1:192.168.100.11/24")
	flag.Parse()
	if a.Dir == "" || out == "" {
		log.Fatal("--artifacts and --out are required")
	}
	a.ConsoleArg = consoleFor(a.Arch)
	if dev {
		a.ConsoleArg += " kuberoot.dev kuberoot.nameserver=1.1.1.1"
	}
	if extra != "" {
		a.ConsoleArg += " " + extra
	}
	var t *trust
	if trustDir != "" {
		t = &trust{dir: trustDir}
		if err := t.ensure(); err != nil {
			log.Fatalf("trust: %v", err)
		}
	}
	if err := build(a, out, t); err != nil {
		log.Fatal(err)
	}
}

func consoleFor(arch string) string {
	if arch == "amd64" {
		return "console=tty0 console=ttyS0"
	}
	return "console=tty0 console=ttyAMA0"
}

func build(a bootdisk.Artifacts, out string, t *trust) error {
	files := map[string]string{
		a.RemovableBootPath():       a.Dir + "/systemd-boot.efi",
		"/kuberoot/vmlinuz.efi":     a.Dir + "/vmlinuz.efi",
		"/kuberoot/initrd.cpio":     a.Dir + "/initrd.cpio",
		"/kuberoot/rootfs.squashfs": a.Dir + "/rootfs.squashfs",
	}
	var content int64
	for _, src := range files {
		st, err := os.Stat(src)
		if err != nil {
			return err
		}
		content += st.Size()
	}
	espSize := (content/bootdisk.MiB + 96) * bootdisk.MiB // FAT32 overhead and headroom

	_ = os.Remove(out)
	d, err := diskfs.Create(out, espSize+2*bootdisk.MiB, diskfs.SectorSizeDefault)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Partition(bootdisk.MediaTable(espSize)); err != nil {
		return fmt.Errorf("partition: %w", err)
	}
	fs, err := d.CreateFilesystem(disk.FilesystemSpec{Partition: 1, FSType: filesystem.TypeFat32, VolumeLabel: "KUBEROOT"})
	if err != nil {
		return fmt.Errorf("format: %w", err)
	}
	for dst, src := range files {
		if err := bootdisk.CopyFile(fs, dst, src); err != nil {
			return err
		}
	}
	install := bootdisk.Entry{
		Title:   "kuberoot " + a.Version + " installer",
		Version: a.Version,
		Kernel:  "/kuberoot/vmlinuz.efi",
		Initrd:  "/kuberoot/initrd.cpio",
		Options: strings.Join([]string{
			"kuberoot.root=PARTLABEL=" + bootdisk.MediaLabel + ":/kuberoot/rootfs.squashfs",
			"kuberoot.mode=install", "quiet", a.ConsoleArg,
		}, " "),
	}
	if err := bootdisk.WriteFile(fs, "/loader/entries/kuberoot-install.conf", strings.NewReader(install.String())); err != nil {
		return err
	}
	if err := bootdisk.WriteFile(fs, "/loader/loader.conf", strings.NewReader(bootdisk.LoaderConf)); err != nil {
		return err
	}
	if t != nil {
		if err := t.install(fs); err != nil {
			return err
		}
		fmt.Printf("admin access: %s\n", t.path("admin.kubeconfig"))
	}
	fmt.Printf("media: %s (%d MiB)\n", out, (espSize+2*bootdisk.MiB)/bootdisk.MiB)
	return nil
}
