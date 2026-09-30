package main

// netconsole.go sends the kernel log over the network when asked to, for a
// machine that has no serial port to read it from.
//
// Without a serial port a stuck DSM boot leaves nothing to look at: the screen
// shows no console, and the copy on the loader disk stops wherever the loader
// last wrote it. The kernel's netconsole sends every printk as a UDP packet,
// and everything systemd logs goes through printk, so with it the whole boot
// can be read from another machine on the same network:
//
//	nc -klu 6666        (or: socat -u udp-recv:6666 -)
//
// It is off unless the kernel command line has vibeldr_netconsole. With no
// value the log is broadcast to UDP port 6666 on the first ethernet interface,
// from a link-local address made from its MAC, so nothing has to be known about
// the network in advance. With a value, the value is handed to the module as
// its netconsole= parameter unchanged, e.g.
//
//	vibeldr_netconsole=6665@192.168.0.114/eth0,6666@192.168.0.254/
//
// It only sees what is logged after it loads. The console log level is raised
// so that informational messages - which is what systemd's are - are sent too.
//
// netconsole.go - 요청이 있으면 커널 로그를 네트워크로 보낸다. 시리얼 포트가
// 없어 거기서 읽을 수 없는 기계를 위한 것이다.
//
// 시리얼 포트가 없으면 멈춘 DSM 부팅은 볼 것을 하나도 남기지 않는다. 화면에는
// 콘솔이 없고, 로더 디스크의 사본은 로더가 마지막으로 쓴 곳에서 끝난다.
// 커널의 netconsole 은 printk 하나하나를 UDP 패킷으로 보내고, systemd 가
// 남기는 것도 전부 printk 를 거치므로, 이게 있으면 같은 네트워크의 다른
// 기계에서 부팅 전체를 읽을 수 있다:
//
//	nc -klu 6666        (또는: socat -u udp-recv:6666 -)
//
// 커널 커맨드라인에 vibeldr_netconsole 이 없으면 꺼져 있다. 값이 없으면 첫
// 이더넷 인터페이스에서 UDP 6666 으로 브로드캐스트한다. 보내는 주소는 그
// MAC 으로 만든 링크 로컬 주소라 네트워크에 대해 미리 알 필요가 없다. 값이
// 있으면 그 값을 모듈의 netconsole= 파라미터로 그대로 넘긴다. 예:
//
//	vibeldr_netconsole=6665@192.168.0.114/eth0,6666@192.168.0.254/
//
// 로드된 뒤에 찍히는 것만 보인다. systemd 의 메시지는 정보 수준이라, 그것까지
// 가도록 콘솔 로그 레벨을 올린다.

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"vibeldr/internal/kmod"
)

const (
	netconsoleFlag   = "vibeldr_netconsole"
	netconsoleModule = "netconsole"
	netconsolePort   = 6666
)

// netconsoleImage is the driver pack's netconsole.ko, kept by loadExtraDrivers.
// netconsoleImage - loadExtraDrivers 가 남겨 둔 드라이버 팩의 netconsole.ko.
var netconsoleImage []byte

// startNetconsole loads netconsole if the command line asks for it.
// startNetconsole - 커맨드라인이 요청하면 netconsole 을 올린다.
func startNetconsole() {
	value, present := cmdlineOption(netconsoleFlag)
	if !present {
		return
	}
	target := value
	if target == "" {
		iface, mac := firstEthernet()
		if iface == "" {
			logf("netconsole: no ethernet interface to send from")
			return
		}
		target = fmt.Sprintf("6665@169.254.%d.%d/%s,%d@255.255.255.255/",
			mac[4]|1, mac[5], iface, netconsolePort)
	}
	// Everything up to debug goes to the consoles, netconsole among them.
	// 디버그까지 전부 콘솔로 - netconsole 도 그중 하나다.
	_ = os.WriteFile("/proc/sys/kernel/printk", []byte("8 4 1 7\n"), 0o644)
	params := "netconsole=" + target
	// The ramdisk's own copy if it has one, otherwise the driver pack's, which
	// loadExtraDrivers kept - the pack lives on a partition with no filesystem,
	// so its modules are never files under /lib/modules.
	//
	// 램디스크에 자기 사본이 있으면 그것, 없으면 loadExtraDrivers 가 남겨 둔
	// 드라이버 팩의 것. 팩은 파일시스템 없는 파티션에 있어서 그 모듈은
	// /lib/modules 아래 파일로 존재하지 않는다.
	var err error
	if index, e := kmod.Scan(kmod.SearchDirs...); e == nil && len(index.ByName(netconsoleModule)) > 0 {
		err = kmod.LoadWith(index.ByName(netconsoleModule)[0], params)
	} else if len(netconsoleImage) > 0 {
		err = kmod.LoadImageWith(netconsoleImage, params)
	} else {
		logf("netconsole: module not found")
		return
	}
	if err != nil {
		logf("netconsole: %v", err)
		return
	}
	logf("netconsole: kernel log to %s", target)
}

// firstEthernet returns the first ethernet interface by name, and its MAC.
// firstEthernet - 이름순으로 첫 이더넷 인터페이스와 그 MAC.
func firstEthernet() (string, net.HardwareAddr) {
	paths, _ := filepath.Glob("/sys/class/net/*/type")
	sort.Strings(paths)
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil || strings.TrimSpace(string(b)) != "1" { // ARPHRD_ETHER / 이더넷
			continue
		}
		dir := filepath.Dir(p)
		a, err := os.ReadFile(filepath.Join(dir, "address"))
		if err != nil {
			continue
		}
		mac, err := net.ParseMAC(strings.TrimSpace(string(a)))
		if err != nil || len(mac) != 6 {
			continue
		}
		return filepath.Base(dir), mac
	}
	return "", nil
}
