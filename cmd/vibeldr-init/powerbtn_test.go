package main

import (
	"encoding/binary"
	"testing"
)

// nlAttr encodes one netlink attribute, padded to four bytes.
// nlAttr - netlink 속성 하나를 4 바이트 맞춤으로 인코딩한다.
func nlAttr(typ uint16, payload []byte) []byte {
	n := 4 + len(payload)
	b := make([]byte, (n+3)&^3)
	binary.LittleEndian.PutUint16(b[0:2], uint16(n))
	binary.LittleEndian.PutUint16(b[2:4], typ)
	copy(b[4:], payload)
	return b
}

// nlMsg wraps generic netlink attributes in the netlink and genl headers.
// nlMsg - generic netlink 속성을 netlink 헤더와 genl 헤더로 감싼다.
func nlMsg(msgType uint16, cmd byte, attrs ...[]byte) []byte {
	body := []byte{cmd, 1, 0, 0}
	for _, a := range attrs {
		body = append(body, a...)
	}
	b := make([]byte, nlmsgHdrLen, nlmsgHdrLen+len(body))
	b = append(b, body...)
	binary.LittleEndian.PutUint32(b[0:4], uint32(len(b)))
	binary.LittleEndian.PutUint16(b[4:6], msgType)
	return b
}

func u16(v uint16) []byte { b := make([]byte, 2); binary.LittleEndian.PutUint16(b, v); return b }
func u32(v uint32) []byte { b := make([]byte, 4); binary.LittleEndian.PutUint32(b, v); return b }

// The controller's answer carries the family id and a nested group list; the
// acpi_mc_group id is picked out of it even when another group comes first.
//
// 컨트롤러 답에는 패밀리 id 와 중첩된 그룹 목록이 있다. 다른 그룹이 앞에 와도
// acpi_mc_group 의 id 를 골라낸다.
func TestACPIFamily(t *testing.T) {
	groups := append(
		nlAttr(1, append(nlAttr(ctrlAttrGrpID, u32(3)), nlAttr(ctrlAttrGrpName, []byte("other\x00"))...)),
		nlAttr(2, append(nlAttr(ctrlAttrGrpID, u32(7)), nlAttr(ctrlAttrGrpName, []byte("acpi_mc_group\x00"))...))...)
	reply := nlMsg(genlIDCtrl, 1,
		nlAttr(ctrlAttrFamName, []byte("acpi_event\x00")),
		nlAttr(ctrlAttrFamID, u16(24)),
		nlAttr(ctrlAttrGroups, groups))
	msgs := splitNetlink(append(reply, reply...))
	if len(msgs) != 2 {
		t.Fatalf("split into %d messages", len(msgs))
	}
	fam, grp, err := acpiFamily(msgs[0])
	if err != nil || fam != 24 || grp != 7 {
		t.Fatalf("family %d group %d err %v", fam, grp, err)
	}
	if _, _, err := acpiFamily(nlMsg(genlIDCtrl, 1, nlAttr(ctrlAttrFamID, u16(24)))); err == nil {
		t.Fatal("a reply without the group was accepted")
	}
}

// An acpi_genl_event for the power button reads as "button/power"; the bus id
// after the class does not leak into it.
//
// 전원 버튼의 acpi_genl_event 는 "button/power" 로 읽힌다. 분류 뒤의 버스 id 가
// 섞여 들어오지 않는다.
func TestACPIEventClass(t *testing.T) {
	ev := make([]byte, 44)
	copy(ev[0:20], "button/power")
	copy(ev[20:35], "LNXPWRBN:00")
	binary.LittleEndian.PutUint32(ev[36:40], 0x80)
	cls, ok := acpiEventClass(nlMsg(24, 1, nlAttr(acpiAttrEvent, ev)))
	if !ok || cls != powerButtonCls {
		t.Fatalf("class %q ok %v", cls, ok)
	}
	if _, ok := acpiEventClass(nlMsg(24, 1, nlAttr(acpiAttrEvent, []byte("short")))); ok {
		t.Fatal("a short event was accepted")
	}
}
