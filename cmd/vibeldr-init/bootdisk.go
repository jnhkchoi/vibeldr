package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Keeping DSM from treating the loader's own disk as an external USB disk.
//
// On a Synology box the kernel names the boot device synoboot. Here the loader
// is an ordinary USB disk to DSM's kernel, which names it sdq or sdu (usb1 on
// SA6400, whose kernel names USB disks usbN), and the
// helper makes the /dev/synoboot nodes for it by hand (makeBootDevice). DSM's
// udev rules ask `synodiskport -portcheck` what kind of disk each block device
// is (05-system-env.rules), and for that name it answers USB. usb.sh then hands
// the disk to hotplugd as external storage ("USB Disk 1" in its log), which
// mounts what it can of it, and on every boot the mount of its fourth
// partition, which has no file system, fails with a usb_mount_fail
// notification.
//
// So the helper writes one more rule into DSM, run after 05-system-env.rules,
// that gives the loader disk and its partitions SYNOBOOT, synodiskport's own
// type for the boot device. KERNELS matches the disk and, through their
// parent, its partitions, whatever the kernel calls them. usb.sh leaves every
// type but USB and USBHUB alone, and no rule in DSM 7.4.1 acts on SYNOBOOT.
//
// The kernel name is found in the ramdisk and does not change until the next
// boot, so the rule is written again on every boot, from bootDiskFile.
//
// DSM 이 로더 자신의 디스크를 외장 USB 디스크로 다루지 않게 한다.
//
// 시놀로지 기계에서는 커널이 부트 장치에 synoboot 라는 이름을 붙인다. 여기서
// 로더는 DSM 커널에게 평범한 USB 디스크라 sdq 나 sdu 같은 이름(USB 디스크를
// usbN 으로 부르는 SA6400 에서는 usb1)을 받고, 헬퍼가 /dev/synoboot 노드를
// 손으로 만든다 (makeBootDevice). DSM 의 udev 규칙은 블록
// 장치마다 `synodiskport -portcheck` 로 어떤 디스크인지 묻는데
// (05-system-env.rules), 그 이름에 대해서는 USB 라고 답한다. 그러면 usb.sh 가
// 디스크를 외장 저장소로 hotplugd 에 넘기고 (로그에 "USB Disk 1"), hotplugd 는
// 마운트할 수 있는 것을 마운트하며, 부팅마다 파일시스템이 없는 네 번째 파티션의
// 마운트가 실패해 usb_mount_fail 알림이 뜬다.
//
// 그래서 헬퍼가 DSM 에 규칙을 하나 더 쓴다. 05-system-env.rules 다음에 돌아
// 로더 디스크와 그 파티션에 SYNOBOOT 를 준다. synodiskport 가 부트 장치에 쓰는
// 자기 종류다. KERNELS 는 디스크와, 부모를 거쳐 그 파티션을 맞춘다. 커널이
// 파티션을 무엇이라 부르든 상관없다. usb.sh 는 USB 와 USBHUB 가 아닌 종류는
// 건드리지 않고, DSM 7.4.1 에는 SYNOBOOT 에 반응하는 규칙이 없다.
//
// 커널 이름은 램디스크에서 알게 되고 다음 부팅까지 바뀌지 않으므로, 규칙은
// bootDiskFile 에서 부팅마다 다시 쓴다.

const (
	// bootDiskFile holds the loader disk's kernel name for the pivot, which
	// runs without /sys.
	//
	// bootDiskFile - pivot 을 위해 로더 디스크의 커널 이름을 담는다. pivot 은
	// /sys 없이 돈다.
	bootDiskFile = "/vibeldr-bootdisk"

	// synobootRuleDir and synobootRuleFile are where the rule goes inside DSM.
	// synobootRuleDir, synobootRuleFile - DSM 안에서 규칙이 놓이는 곳.
	synobootRuleDir  = "/etc/udev/rules.d"
	synobootRuleFile = synobootRuleDir + "/06-vibeldr-synoboot.rules"
)

// diskName is what a kernel disk name looks like; nothing else goes into the
// rule.
//
// diskName - 커널 디스크 이름의 모양. 이것 말고는 규칙에 들어가지 않는다.
var diskName = regexp.MustCompile(`^[a-z]+[a-z0-9]*$`)

// saveBootDisk records the loader disk's kernel name.
// saveBootDisk - 로더 디스크의 커널 이름을 적어 둔다.
func saveBootDisk(disk string) {
	if err := os.WriteFile(bootDiskFile, []byte(disk+"\n"), 0o644); err != nil {
		logf("boot device: %s: %v", bootDiskFile, err)
	}
}

// synobootRuleText is the rule for disk.
// synobootRuleText - disk 에 대한 규칙.
func synobootRuleText(disk string) string {
	return fmt.Sprintf("# vibeldr: %s is the loader's own disk, DSM's boot device (cmd/vibeldr-init/bootdisk.go).\n"+
		"# vibeldr: %s 는 로더 자신의 디스크, DSM 의 부트 장치다 (cmd/vibeldr-init/bootdisk.go).\n"+
		"SUBSYSTEM==\"block\", KERNELS==\"%s\", ENV{SYNO_DEV_DISKPORTTYPE}=\"SYNOBOOT\"\n",
		disk, disk, disk)
}

// installSynobootRule writes the rule into the installed system under root, or
// removes an old one when the loader disk is not known this boot.
//
// installSynobootRule - root 아래 설치된 시스템에 규칙을 쓴다. 이번 부팅에 로더
// 디스크를 모르면 옛 규칙을 지운다.
func installSynobootRule(root string) {
	raw, err := os.ReadFile(bootDiskFile)
	disk := strings.TrimSpace(string(raw))
	if err != nil || !diskName.MatchString(disk) {
		_ = os.Remove(root + synobootRuleFile)
		return
	}
	if err := os.MkdirAll(root+synobootRuleDir, 0o755); err != nil {
		logf("synoboot rule: %v", err)
		return
	}
	if err := os.WriteFile(root+synobootRuleFile, []byte(synobootRuleText(disk)), 0o644); err != nil {
		logf("synoboot rule: %v", err)
		return
	}
	logf("synoboot rule: %s marked as the boot device for DSM", disk)
}
