package kpatch

import (
	"testing"
)

func TestApply_CodeMode_RefusesUnexpectedBytes(t *testing.T) {
	// An original that differs from Expect gives Skipped, which keeps code from
	// being wiped at the wrong coordinates.
	//
	// Expect 와 다른 원본이면 Skipped. 잘못된 좌표에 코드가 지워지는 사고를 막는다.
	buf := make([]byte, 0x100)
	// The original is mov eax, 5 (B8 05 00 00 00), which differs from Expect.
	// 원본은 mov eax, 5 (B8 05 00 00 00) - Expect 와 다르다.
	copy(buf[0x40:], []byte{0xB8, 0x05, 0x00, 0x00, 0x00})
	v := &VMLinux{Bytes: buf}
	site := Site{
		Name: "root_move_eperm", FileOff: 0x40, Length: 5,
		Data:     []byte{0xB8, 0x00, 0x00, 0x00, 0x00},
		Expect:   []byte{0xB8, 0xFF, 0xFF, 0xFF, 0xFF},
		CodeMode: true,
		Why:      "test",
	}
	r := Apply(v, []Site{site})
	if len(r.Applied) != 0 || len(r.Skipped) != 1 {
		t.Fatalf("Applied=%d Skipped=%d", len(r.Applied), len(r.Skipped))
	}
	if buf[0x40] != 0xB8 || buf[0x41] != 0x05 {
		t.Errorf("원본이 손상됨: %x", buf[0x40:0x45])
	}
}

func TestApply_CodeMode_IdempotentOnAlreadyPatched(t *testing.T) {
	// Already overwritten by us and matching Data, so reapplying is harmless and
	// gives Applied.
	//
	// 이미 우리가 덮어쓴 상태 (Data 와 일치) 라면 재적용은 무해하므로 Applied.
	buf := make([]byte, 0x100)
	copy(buf[0x40:], []byte{0xB8, 0x00, 0x00, 0x00, 0x00})
	v := &VMLinux{Bytes: buf}
	site := Site{
		Name: "root_move_eperm", FileOff: 0x40, Length: 5,
		Data:     []byte{0xB8, 0x00, 0x00, 0x00, 0x00},
		Expect:   []byte{0xB8, 0xFF, 0xFF, 0xFF, 0xFF},
		CodeMode: true,
	}
	r := Apply(v, []Site{site})
	if len(r.Applied) != 1 || len(r.Skipped) != 0 {
		t.Fatalf("Applied=%d Skipped=%d", len(r.Applied), len(r.Skipped))
	}
}
