//go:build linux

// rescue_linux_test.go is the smallest unit test for rescue mode's rendering
// and state transitions.
//
// The real btrfs commands are hard to mock and are not covered here. Superblock
// parsing is a pure function and worth checking for accuracy, and the submenu
// code on top of it is nothing but fmt.Print, so a static check stands in for it.
//
// rescue_linux_test.go - rescue 모드 렌더링/상태전이 최소 유닛테스트.
//
// btrfs 실 명령은 mock 이 어려워 여기선 다루지 않는다. superblock 파싱은
// 순수 함수라 정확도 검증이 의미 있고, 그 위에 얹힌 서브메뉴 코드는
// (fmt.Print 뿐이라) 정적 검사로 대체한다.
package main

import (
	"encoding/binary"
	"testing"
)

// TestParseBTRFSSuperMagic: whether the magic, generation, label and fsid come
// out at their offsets.
//
// TestParseBTRFSSuperMagic - magic / generation / label / fsid 가
// 오프셋대로 뽑히는지 본다.
func TestParseBTRFSSuperMagic(t *testing.T) {
	buf := make([]byte, btrfsSuperSize)
	copy(buf[btrfsMagicOff:], []byte(btrfsMagic))
	binary.LittleEndian.PutUint64(buf[btrfsGenerationOff:], 4242)
	label := "my-nas-volume"
	copy(buf[btrfsLabelOff:], []byte(label))
	for i := 0; i < 16; i++ {
		buf[btrfsFsidOff+i] = byte(i + 1)
	}

	s := parseBTRFSSuper("/dev/sdx1", buf)
	if s == nil {
		t.Fatal("expected non-nil parsed super")
	}
	if s.Label != label {
		t.Errorf("label: got %q want %q", s.Label, label)
	}
	if s.Generation != 4242 {
		t.Errorf("generation: got %d want 4242", s.Generation)
	}
	if s.FSID[0] != 1 || s.FSID[15] != 16 {
		t.Errorf("fsid unexpected: %x", s.FSID)
	}
	if s.Device != "/dev/sdx1" {
		t.Errorf("device: got %q want /dev/sdx1", s.Device)
	}
}

// TestParseBTRFSSuperWrongMagic: a mismatched magic gives nil. It checks that
// volume detection's "no btrfs here" verdict is accurate.
//
// TestParseBTRFSSuperWrongMagic - magic 이 안 맞으면 nil.
// 볼륨 감지의 "여기 btrfs 없음" 판정이 정확한지 본다.
func TestParseBTRFSSuperWrongMagic(t *testing.T) {
	buf := make([]byte, btrfsSuperSize)
	copy(buf[btrfsMagicOff:], []byte("XXXXXXXX"))
	if s := parseBTRFSSuper("/dev/sdx1", buf); s != nil {
		t.Errorf("expected nil for wrong magic, got %+v", s)
	}
}

// TestParseBTRFSSuperShortBuf: too short gives nil, without panicking.
// TestParseBTRFSSuperShortBuf - 너무 짧으면 nil (panic 이 안 난다).
func TestParseBTRFSSuperShortBuf(t *testing.T) {
	if s := parseBTRFSSuper("/dev/sdx1", make([]byte, 64)); s != nil {
		t.Errorf("expected nil for short buf, got %+v", s)
	}
}

// TestIsAllDigits covers the snapshot ID input filter.
// TestIsAllDigits - snapshot ID 입력 필터.
func TestIsAllDigits(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"", false},
		{"0", true},
		{"12345", true},
		{"12a", false},
		{"-1", false},
		{" 5", false},
	}
	for _, c := range cases {
		if got := isAllDigits(c.s); got != c.want {
			t.Errorf("isAllDigits(%q) = %v, want %v", c.s, got, c.want)
		}
	}
}
