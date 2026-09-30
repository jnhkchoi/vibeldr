package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
)

// Turning the ACPI power button into a DSM shutdown.
//
// On Synology's own boxes the button is wired to their board; on any other
// machine it is the ACPI power button, and DSM 7.4.1 has nothing listening for
// that: none of the three models ships acpid or /etc/acpi, and their kernels
// have no evdev. A press - or a hypervisor's "Shutdown", which is the same
// event - is dropped, and the machine keeps running.
//
// The kernel still reports the press. The ACPI button driver (button.ko: in
// DS918+'s DSM, in the driver pack for all three) sends it over generic
// netlink, family "acpi_event", multicast group "acpi_mc_group", as an
// acpi_genl_event whose device class is "button/power". A small service in DSM
// listens there and runs Synology's own /usr/syno/sbin/synopoweroff, once.
//
// ACPI 전원 버튼을 DSM 종료로 잇는다.
//
// 시놀로지 자신의 기계에서는 버튼이 자기 보드에 연결돼 있다. 다른 기계에서는
// ACPI 전원 버튼인데, DSM 7.4.1 에는 그것을 듣는 것이 없다. 세 모델 모두
// acpid 도 /etc/acpi 도 싣지 않고, 커널에는 evdev 가 없다. 버튼을 누르거나
// 하이퍼바이저의 "Shutdown"(같은 이벤트다)을 보내도 버려지고 기계는 계속 돈다.
//
// 커널은 누름을 여전히 알린다. ACPI 버튼 드라이버(button.ko: DS918+ 는 DSM 에,
// 세 모델 모두 드라이버 팩에 있다)가 generic netlink 의 "acpi_event" 패밀리,
// "acpi_mc_group" 멀티캐스트 그룹으로 장치 분류가 "button/power" 인
// acpi_genl_event 를 보낸다. DSM 안의 작은 서비스가 거기서 듣다가 시놀로지
// 자신의 /usr/syno/sbin/synopoweroff 를 한 번 돌린다.

const (
	acpiFamilyName  = "acpi_event"
	acpiGroupName   = "acpi_mc_group"
	powerButtonCls  = "button/power"
	synoPowerOff    = "/usr/syno/sbin/synopoweroff"
	powerUnitName   = "vibeldr-power.service"
	powerModeFlag   = "-power"
	nlmsgHdrLen     = 16
	genlHdrLen      = 4
	genlIDCtrl      = 0x10
	ctrlCmdGetFam   = 3
	ctrlAttrFamName = 2
	ctrlAttrFamID   = 1
	ctrlAttrGroups  = 7
	ctrlAttrGrpName = 1
	ctrlAttrGrpID   = 2
	acpiAttrEvent   = 1
	nlaTypeMask     = 0x3fff
)

// netlinkAttrs splits a run of netlink attributes into type -> payload. A
// truncated or malformed attribute ends the walk.
//
// netlinkAttrs - netlink 속성 묶음을 종류 -> 내용으로 나눈다. 잘렸거나 틀린
// 속성을 만나면 거기서 멈춘다.
func netlinkAttrs(b []byte) map[uint16][]byte {
	out := map[uint16][]byte{}
	for len(b) >= 4 {
		n := int(binary.LittleEndian.Uint16(b[0:2]))
		typ := binary.LittleEndian.Uint16(b[2:4]) & nlaTypeMask
		if n < 4 || n > len(b) {
			break
		}
		out[typ] = b[4:n]
		step := (n + 3) &^ 3
		if step > len(b) {
			break
		}
		b = b[step:]
	}
	return out
}

// splitNetlink cuts a read from a netlink socket into its messages, headers
// included. A length that runs past the buffer ends the split.
//
// splitNetlink - netlink 소켓에서 읽은 것을 헤더를 포함한 메시지들로 자른다.
// 버퍼를 넘는 길이를 만나면 거기서 멈춘다.
func splitNetlink(b []byte) [][]byte {
	var out [][]byte
	for len(b) >= nlmsgHdrLen {
		n := int(binary.LittleEndian.Uint32(b[0:4]))
		if n < nlmsgHdrLen || n > len(b) {
			break
		}
		out = append(out, b[:n])
		step := (n + 3) &^ 3
		if step > len(b) {
			break
		}
		b = b[step:]
	}
	return out
}

// acpiFamily reads the family id and the id of acpi_mc_group out of the
// controller's reply to CTRL_CMD_GETFAMILY (one netlink message, headers
// included).
//
// acpiFamily - CTRL_CMD_GETFAMILY 에 대한 컨트롤러 답(헤더 포함 netlink 메시지
// 하나)에서 패밀리 id 와 acpi_mc_group 의 id 를 읽는다.
func acpiFamily(msg []byte) (family uint16, group uint32, err error) {
	if len(msg) < nlmsgHdrLen+genlHdrLen {
		return 0, 0, fmt.Errorf("short reply (%d bytes)", len(msg))
	}
	attrs := netlinkAttrs(msg[nlmsgHdrLen+genlHdrLen:])
	id, ok := attrs[ctrlAttrFamID]
	if !ok || len(id) < 2 {
		return 0, 0, fmt.Errorf("no %s family in the reply", acpiFamilyName)
	}
	family = binary.LittleEndian.Uint16(id)
	for _, g := range netlinkAttrs(attrs[ctrlAttrGroups]) {
		ga := netlinkAttrs(g)
		if string(bytes.TrimRight(ga[ctrlAttrGrpName], "\x00")) == acpiGroupName && len(ga[ctrlAttrGrpID]) >= 4 {
			return family, binary.LittleEndian.Uint32(ga[ctrlAttrGrpID]), nil
		}
	}
	return family, 0, fmt.Errorf("no %s group in the reply", acpiGroupName)
}

// acpiEventClass returns the device class of an acpi_genl_event message
// (struct acpi_genl_event starts with char device_class[20]).
//
// acpiEventClass - acpi_genl_event 메시지의 장치 분류를 돌려준다
// (struct acpi_genl_event 는 char device_class[20] 로 시작한다).
func acpiEventClass(msg []byte) (string, bool) {
	if len(msg) < nlmsgHdrLen+genlHdrLen {
		return "", false
	}
	ev, ok := netlinkAttrs(msg[nlmsgHdrLen+genlHdrLen:])[acpiAttrEvent]
	if !ok || len(ev) < 20 {
		return "", false
	}
	cls := ev[:20]
	if i := bytes.IndexByte(cls, 0); i >= 0 {
		cls = cls[:i]
	}
	return string(cls), true
}

// installPowerService writes the unit that keeps the listener running inside
// DSM and enables it, the same way installAgent does for the agent.
//
// installPowerService - DSM 안에서 리스너를 계속 돌리는 유닛을 쓰고, 에이전트에
// 하듯 enable 한다.
func installPowerService(root string) error {
	unit := fmt.Sprintf(`[Unit]
Description=vibeldr: shut DSM down on the ACPI power button

[Service]
Type=simple
ExecStart=%s %s
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
`, dsmAgentName, powerModeFlag)
	if err := os.MkdirAll(root+dsmWantsDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(root+dsmServiceDir+"/"+powerUnitName, []byte(unit), 0o644); err != nil {
		return err
	}
	link := root + dsmWantsDir + "/" + powerUnitName
	_ = os.Remove(link)
	return os.Symlink(dsmServiceDir+"/"+powerUnitName, link)
}
