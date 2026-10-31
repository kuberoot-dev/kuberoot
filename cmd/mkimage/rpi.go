package main

import (
	"fmt"
	"os"
	"strings"
)

// A Raspberry Pi and a UEFI machine read the same FAT partition and ignore
// each other's files: the Pi firmware looks for config.txt and never opens
// /EFI, while systemd-boot reads /loader and never opens config.txt. So one
// medium boots both, and there is no moment where someone has to pick the
// right image for the board in their hand.
//
// Pi 4 and Pi 5 both read GPT, which is what MediaTable already writes; only
// Pi 3 and older need an MBR, and they are 32-bit-era boards this tree does
// not target.
//
// What each board needs from the partition differs. The Pi 5 keeps its whole
// firmware in EEPROM and wants only config.txt, the kernel and its devicetree.
// The Pi 4's EEPROM is a first stage that then loads start4.elf and fixup4.dat
// from here, so those two blobs have to be on the medium or the board stops
// with a blank screen. They are not ours to build -- scripts/fetch-rpi-firmware.sh
// pins and fetches them.

// rpiFirmware are the files the Pi 4 bootloader loads off the medium. The Pi 5
// needs none of them.
var rpiFirmware = []string{"start4.elf", "fixup4.dat"}

// rpiDevicetrees are the boards this medium can boot, by the filename their
// firmware looks for. The firmware picks one by board revision on its own, so
// both ship and neither is selected in config.txt.
var rpiDevicetrees = []string{"bcm2711-rpi-4-b.dtb", "bcm2712-rpi-5-b.dtb"}

// rpiFiles maps each file's place on the medium to its source, and fails on
// anything missing rather than producing a medium that looks complete and
// stops on the board with no console output to explain it.
func rpiFiles(dir string) (map[string]string, error) {
	files := map[string]string{
		// The firmware loads the kernel by the name config.txt gives, and that
		// kernel is a plain Image: the firmware knows nothing of the EFI zboot
		// wrapper vmlinuz.efi carries for the virtual machines.
		"/Image":       dir + "/Image",
		"/initrd.cpio": dir + "/initrd.cpio",
	}
	for _, dtb := range rpiDevicetrees {
		files["/"+dtb] = dir + "/dtb/" + dtb
	}
	for _, f := range rpiFirmware {
		files["/"+f] = dir + "/firmware/" + f
	}
	for dst, src := range files {
		if _, err := os.Stat(src); err != nil {
			if strings.Contains(dst, ".elf") || strings.Contains(dst, ".dat") {
				return nil, fmt.Errorf("%s is missing: run scripts/fetch-rpi-firmware.sh to fetch the Pi 4 firmware into %s/firmware", src, dir)
			}
			return nil, fmt.Errorf("%s is missing: %w", src, err)
		}
	}
	return files, nil
}

// rpiConfig is config.txt: what the firmware reads before anything else.
//
// The conditional filters matter. A dtparam the board does not know is a
// warning the firmware prints to a console that is not connected yet, so a
// line meant for one board has to be fenced off from the other rather than
// left to be ignored.
func rpiConfig() string {
	return strings.Join([]string{
		"# Written by mkimage. The EFI loader beside this file is for UEFI",
		"# machines; the Pi firmware reads this and ignores it.",
		"[all]",
		"arm_64bit=1",
		"kernel=Image",
		// followkernel places the initramfs straight after the kernel in
		// memory, which is the only placement that needs no address picked by
		// hand -- and an address picked by hand is one that is wrong on the
		// board with a different memory size.
		"initramfs initrd.cpio followkernel",
		"enable_uart=1",
		"",
		"[pi5]",
		// The PCIe connector is off until asked for, and the storage pools
		// live on the NVMe drive behind it: without this the board boots with
		// nowhere to put data.
		"dtparam=pciex1",
		"",
		"[all]",
		"",
	}, "\n")
}

// rpiCmdline is cmdline.txt, which the firmware hands to the kernel. It has to
// be one line: the firmware passes the file through as written, so a second
// line is silently dropped along with every argument on it.
func rpiCmdline(options string) string {
	return strings.Join(strings.Fields(options), " ") + "\n"
}
