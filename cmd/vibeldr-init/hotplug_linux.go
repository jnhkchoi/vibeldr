//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"vibeldr/internal/kmod"
)

// Drivers for USB devices plugged in while DSM runs.
//
// The helper loads what the machine needs from the driver pack once, in the
// ramdisk, for the devices present then. DSM's own udev rules load no module
// for a device that turns up later - none of them runs kmod - so a USB network
// card or other USB device plugged in afterwards stays without a driver unless
// one is already loaded.
//
// So the helper leaves a udev rule in DSM (hotplugRule) that runs the agent for
// every USB interface that appears. The agent waits a moment for a driver
// already in the kernel to take it, and otherwise reads the pack off the loader
// disk and loads what matches, the way the ramdisk stage does. What it did goes
// to hotplugLog.
//
// DSM 이 도는 중에 꽂은 USB 장치의 드라이버.
//
// 헬퍼는 램디스크에서 한 번, 그때 있는 장치에 필요한 것을 드라이버 팩에서
// 올린다. DSM 자신의 udev 규칙은 나중에 나타난 장치에 모듈을 올리지 않는다 -
// kmod 를 부르는 규칙이 없다. 그래서 그 뒤에 꽂은 USB 랜카드나 다른 USB 장치는
// 이미 올라온 드라이버가 없으면 드라이버 없이 남는다.
//
// 그래서 헬퍼가 DSM 에 udev 규칙(hotplugRule)을 남겨, 나타나는 USB 인터페이스마다
// 에이전트를 부른다. 에이전트는 커널에 이미 있는 드라이버가 가져가기를 잠깐
// 기다리고, 아니면 로더 디스크에서 팩을 읽어 맞는 것을 램디스크 단계처럼 올린다.
// 한 일은 hotplugLog 에 남는다.

const (
	// hotplugRule is where the rule goes inside DSM.
	// hotplugRule - DSM 안에서 규칙이 놓이는 곳.
	hotplugRule = synobootRuleDir + "/80-vibeldr-hotplug.rules"
	// hotplugLog is what the agent did, one line per device.
	// hotplugLog - 에이전트가 한 일. 장치마다 한 줄.
	hotplugLog = "/var/log/vibeldr-hotplug.log"
	// dsmBootDisk holds the loader disk's kernel name inside DSM, copied from
	// bootDiskFile, for finding the pack.
	//
	// dsmBootDisk - DSM 안에서 로더 디스크의 커널 이름. 팩을 찾으려고
	// bootDiskFile 에서 복사한다.
	dsmBootDisk = notifyDSMDir + "/bootdisk"
	// hotplugLock keeps two agents from loading at the same time: a device
	// with several interfaces sends an event for each.
	//
	// hotplugLock - 에이전트 둘이 동시에 올리지 않게 한다. 인터페이스가 여러
	// 개인 장치는 인터페이스마다 이벤트를 보낸다.
	hotplugLock = "/run/vibeldr-hotplug.lock"
)

// hotplugRuleText is the rule: the agent for every USB interface added.
// hotplugRuleText - 규칙. 더해지는 USB 인터페이스마다 에이전트를 부른다.
func hotplugRuleText() string {
	return "# vibeldr: drivers for USB devices plugged in while DSM runs (cmd/vibeldr-init/hotplug_linux.go).\n" +
		"# vibeldr: DSM 이 도는 중에 꽂은 USB 장치의 드라이버 (cmd/vibeldr-init/hotplug_linux.go).\n" +
		"ACTION==\"add\", SUBSYSTEM==\"usb\", ENV{DEVTYPE}==\"usb_interface\", ENV{MODALIAS}==\"?*\", RUN+=\"" +
		dsmAgentName + " -hotplug $env{MODALIAS} $devpath\"\n"
}

// installHotplug writes the rule and the loader disk's name into the installed
// system under root.
//
// installHotplug - root 아래 설치된 시스템에 규칙과 로더 디스크 이름을 쓴다.
func installHotplug(root string) {
	raw, err := os.ReadFile(bootDiskFile)
	disk := strings.TrimSpace(string(raw))
	if err != nil || !diskName.MatchString(disk) {
		_ = os.Remove(root + hotplugRule)
		return
	}
	for _, d := range []string{synobootRuleDir, notifyDSMDir} {
		if err := os.MkdirAll(root+d, 0o755); err != nil {
			logf("hotplug: %v", err)
			return
		}
	}
	if err := os.WriteFile(root+dsmBootDisk, []byte(disk+"\n"), 0o644); err != nil {
		logf("hotplug: %v", err)
		return
	}
	if err := os.WriteFile(root+hotplugRule, []byte(hotplugRuleText()), 0o644); err != nil {
		logf("hotplug: %v", err)
	}
}

// runHotplug is -hotplug: find a driver for the USB interface at devpath
// (relative to /sys) with modalias.
//
// runHotplug - -hotplug. devpath (/sys 기준) 의 USB 인터페이스에, modalias 로
// 드라이버를 찾아 준다.
func runHotplug(modalias, devpath string) error {
	driver := filepath.Join("/sys", devpath, "driver")
	if waitForPath(driver, 3*time.Second) {
		return nil
	}

	lock, err := os.OpenFile(hotplugLock, os.O_CREATE|os.O_RDWR, 0o600)
	if err == nil {
		defer lock.Close()
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX)
	}
	// Another agent may have loaded the driver while this one waited.
	// 기다리는 사이 다른 에이전트가 드라이버를 올렸을 수 있다.
	if _, err := os.Stat(driver); err == nil {
		return nil
	}

	raw, err := os.ReadFile(dsmBootDisk)
	if err != nil {
		return hotplugNote(modalias, fmt.Errorf("loader disk not known: %w", err))
	}
	dev, err := partitionNodeOf(strings.TrimSpace(string(raw)), 4, packDevice)
	if err != nil {
		return hotplugNote(modalias, err)
	}
	defer os.Remove(dev)
	pk, err := readPack(dev)
	if err != nil {
		return hotplugNote(modalias, err)
	}
	order := pk.index.ResolvePreferring([]string{modalias}, kmod.Loaded("/proc/modules"), pk.originals)
	if len(order) == 0 {
		return hotplugNote(modalias, fmt.Errorf("no driver in the pack"))
	}
	var loaded, failed []string
	for _, m := range order {
		placeFirmware(m, pk.firmware)
		if err := kmod.LoadImage(pk.images[m.Name]); err != nil {
			failed = append(failed, m.Name+" ("+err.Error()+")")
			continue
		}
		loaded = append(loaded, m.Name)
	}
	if _, err := os.Stat(driver); err != nil && len(failed) > 0 {
		return hotplugNote(modalias, fmt.Errorf("loaded %v, failed %v", loaded, failed))
	}
	return hotplugNote(modalias, fmt.Errorf("loaded %v", loaded))
}

// hotplugNote appends one line to hotplugLog and returns nil: the outcome is
// the log line, and udev has nothing to do with an error.
//
// hotplugNote - hotplugLog 에 한 줄을 더하고 nil 을 돌려준다. 결과는 로그 줄이고,
// udev 는 오류로 할 일이 없다.
func hotplugNote(modalias string, what error) error {
	f, err := os.OpenFile(hotplugLog, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s: %v\n", time.Now().Format(time.RFC3339), modalias, what)
	return nil
}

// waitForPath waits up to d for p to exist.
// waitForPath - p 가 생길 때까지 최대 d 만큼 기다린다.
func waitForPath(p string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for {
		if _, err := os.Stat(p); err == nil {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// partitionNodeOf makes node for partition n of disk from its numbers in
// sysfs and returns it. The caller removes it.
//
// partitionNodeOf - disk 의 n 번 파티션 노드를 sysfs 의 번호로 node 에 만들어
// 돌려준다. 호출자가 지운다.
func partitionNodeOf(disk string, n int, node string) (string, error) {
	entries, err := os.ReadDir(filepath.Join("/sys/block", disk))
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		dir := filepath.Join("/sys/block", disk, e.Name())
		if raw, err := os.ReadFile(filepath.Join(dir, "partition")); err != nil || strings.TrimSpace(string(raw)) != strconv.Itoa(n) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, "dev"))
		if err != nil {
			return "", err
		}
		var major, minor uint32
		if _, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d:%d", &major, &minor); err != nil {
			return "", err
		}
		_ = os.Remove(node)
		if err := syscall.Mknod(node, syscall.S_IFBLK|0o600, int(mkdev(major, minor))); err != nil {
			return "", err
		}
		return node, nil
	}
	return "", fmt.Errorf("%s has no partition %d", disk, n)
}
