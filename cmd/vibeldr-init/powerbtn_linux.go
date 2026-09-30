//go:build linux

package main

import (
	"encoding/binary"
	"fmt"
	"os/exec"
	"syscall"
	"time"
)

// powerIgnoreAfter is how long presses are ignored once a shutdown has been
// started. Some boards fire the button twice for one press, and a second
// synopoweroff in the middle of the first one's shutdown helps nobody.
//
// powerIgnoreAfter - 종료를 시작한 뒤 누름을 무시하는 시간. 어떤 보드는 한 번
// 누름에 버튼을 두 번 쏘는데, 첫 종료 도중의 두 번째 synopoweroff 는 누구에게도
// 도움이 안 된다.
const powerIgnoreAfter = 2 * time.Minute

// runPowerButton is the -power mode: join the ACPI event group and run
// synopoweroff on a power button press. It returns only on an error, which
// systemd answers by starting it again.
//
// runPowerButton - -power 모드. ACPI 이벤트 그룹에 들어가 전원 버튼이 눌리면
// synopoweroff 를 돌린다. 오류일 때만 돌아오고, systemd 가 다시 띄운다.
func runPowerButton() error {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW|syscall.SOCK_CLOEXEC, syscall.NETLINK_GENERIC)
	if err != nil {
		return fmt.Errorf("netlink socket: %w", err)
	}
	defer syscall.Close(fd)
	if err := syscall.Bind(fd, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return fmt.Errorf("netlink bind: %w", err)
	}

	family, group, err := askACPIFamily(fd)
	if err != nil {
		return err
	}
	const solNetlink, addMembership = 270, 1
	if err := syscall.SetsockoptInt(fd, solNetlink, addMembership, int(group)); err != nil {
		return fmt.Errorf("join %s: %w", acpiGroupName, err)
	}
	logf("power button: listening (family %d, group %d)", family, group)

	var last time.Time
	buf := make([]byte, 8192)
	for {
		n, _, err := syscall.Recvfrom(fd, buf, 0)
		if err != nil {
			if err == syscall.EINTR {
				continue
			}
			return fmt.Errorf("netlink read: %w", err)
		}
		for _, msg := range splitNetlink(buf[:n]) {
			if binary.LittleEndian.Uint16(msg[4:6]) != family {
				continue
			}
			cls, ok := acpiEventClass(msg)
			if !ok || cls != powerButtonCls {
				continue
			}
			if !last.IsZero() && time.Since(last) < powerIgnoreAfter {
				logf("power button: pressed again, shutdown already under way")
				continue
			}
			last = time.Now()
			logf("power button: pressed, running %s", synoPowerOff)
			if out, err := exec.Command(synoPowerOff).CombinedOutput(); err != nil {
				logf("power button: %s: %v %s", synoPowerOff, err, out)
			}
		}
	}
}

// askACPIFamily sends CTRL_CMD_GETFAMILY for acpi_event and reads the answer.
// askACPIFamily - acpi_event 에 대해 CTRL_CMD_GETFAMILY 를 보내고 답을 읽는다.
func askACPIFamily(fd int) (uint16, uint32, error) {
	name := append([]byte(acpiFamilyName), 0)
	attrLen := 4 + len(name)
	total := nlmsgHdrLen + genlHdrLen + (attrLen+3)&^3
	req := make([]byte, total)
	binary.LittleEndian.PutUint32(req[0:4], uint32(total))
	binary.LittleEndian.PutUint16(req[4:6], genlIDCtrl)
	binary.LittleEndian.PutUint16(req[6:8], syscall.NLM_F_REQUEST)
	binary.LittleEndian.PutUint32(req[8:12], 1)
	req[16] = ctrlCmdGetFam
	req[17] = 1
	binary.LittleEndian.PutUint16(req[20:22], uint16(attrLen))
	binary.LittleEndian.PutUint16(req[22:24], ctrlAttrFamName)
	copy(req[24:], name)
	if err := syscall.Sendto(fd, req, 0, &syscall.SockaddrNetlink{Family: syscall.AF_NETLINK}); err != nil {
		return 0, 0, fmt.Errorf("ask for %s: %w", acpiFamilyName, err)
	}
	buf := make([]byte, 8192)
	n, _, err := syscall.Recvfrom(fd, buf, 0)
	if err != nil {
		return 0, 0, fmt.Errorf("read %s reply: %w", acpiFamilyName, err)
	}
	for _, msg := range splitNetlink(buf[:n]) {
		if binary.LittleEndian.Uint16(msg[4:6]) == syscall.NLMSG_ERROR {
			return 0, 0, fmt.Errorf("no %s family in this kernel", acpiFamilyName)
		}
		return acpiFamily(msg)
	}
	return 0, 0, fmt.Errorf("empty %s reply", acpiFamilyName)
}
