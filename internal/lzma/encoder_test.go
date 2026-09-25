package lzma

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// TestEncodeHeader checks that the header preset "9e" produces has the same
// shape as the DSM original.
// 5D 00 00 00 04 ff ff ff ff ff ff ff ff : lc=3 lp=0 pb=2, dict=64MB, size=unknown.
//
// TestEncodeHeader 는 preset "9e" 로 나온 헤더가 DSM 원본과 같은 형태인지 본다.
// 5D 00 00 00 04 ff ff ff ff ff ff ff ff : lc=3 lp=0 pb=2, dict=64MB, size=unknown.
func TestEncodeHeader(t *testing.T) {
	out, err := Encode([]byte{}, "9e")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if len(out) < HeaderSize {
		t.Fatalf("output %d bytes, want at least %d", len(out), HeaderSize)
	}
	want := []byte{0x5D, 0, 0, 0, 4, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	if !bytes.Equal(out[:HeaderSize], want) {
		t.Errorf("header = %x, want %x", out[:HeaderSize], want)
	}
}

// TestEncodeUnknownPreset: an unknown preset is an error.
// TestEncodeUnknownPreset - 알 수 없는 preset 이면 에러.
func TestEncodeUnknownPreset(t *testing.T) {
	if _, err := Encode(nil, "totally-unknown"); err == nil {
		t.Error("expected error for unknown preset")
	}
}

// TestRoundTripEmpty: a round trip on empty input.
// TestRoundTripEmpty - 빈 입력 왕복.
func TestRoundTripEmpty(t *testing.T) {
	roundTrip(t, nil)
}

// TestRoundTripSmall: a round trip on short literal input, with no matches.
// TestRoundTripSmall - 짧은 리터럴 입력 (매치 없음) 왕복.
func TestRoundTripSmall(t *testing.T) {
	roundTrip(t, []byte("hello, world"))
}

// TestRoundTripRepeated: a round trip on a repeating pattern, to draw out long
// matches.
//
// TestRoundTripRepeated - 반복 패턴 (긴 매치 유도) 왕복.
func TestRoundTripRepeated(t *testing.T) {
	buf := bytes.Repeat([]byte("The quick brown fox jumps over the lazy dog. "), 200)
	roundTrip(t, buf)
}

// TestRoundTripAllBytes: a round trip on 0..255 repeated, for hash hits and
// short matches.
//
// TestRoundTripAllBytes - 0..255 반복 (해시 히트, 짧은 매치) 왕복.
func TestRoundTripAllBytes(t *testing.T) {
	buf := make([]byte, 4096)
	for i := range buf {
		buf[i] = byte(i)
	}
	roundTrip(t, buf)
}

// TestRoundTripRandom: a round trip on incompressible input, almost all
// literals.
//
// TestRoundTripRandom - 무압축 입력 (거의 리터럴만) 왕복.
func TestRoundTripRandom(t *testing.T) {
	buf := make([]byte, 8192)
	if _, err := rand.Read(buf); err != nil {
		t.Fatal(err)
	}
	roundTrip(t, buf)
}

// TestRoundTripLargePattern: a round trip on mixed input of middling size,
// around 256KB, so that the match, rep and literal paths are all exercised.
//
// TestRoundTripLargePattern - 중간 크기 (~256KB) 혼합 입력 왕복. 매치/rep/
// 리터럴 모든 경로가 걸리도록 한다.
func TestRoundTripLargePattern(t *testing.T) {
	buf := make([]byte, 0, 256<<10)
	// Repeating text with some randomness in it, for both a compression ratio and a
	// variety of matches.
	//
	// 임의성 있는 반복 텍스트: 압축률과 매치 다양성 둘 다 확보한다.
	base := []byte("Synology DSM loader vibeldr recompresses vmlinux with a pure Go LZMA encoder.\n")
	rnd := make([]byte, 32)
	for i := 0; len(buf) < 256<<10; i++ {
		buf = append(buf, base...)
		binary.LittleEndian.PutUint32(rnd[:4], uint32(i*2654435761))
		buf = append(buf, rnd[:4]...)
	}
	roundTrip(t, buf)
}

// TestEncodeDeterministic: encoding the same input twice has to give
// byte-identical results, which guarantees no randomness crept into the
// encoder.
//
// TestEncodeDeterministic - 같은 입력을 두 번 인코딩하면 결과가 바이트 단위로
// 같아야 한다. 인코더에 랜덤 성분이 섞이지 않았음을 보장한다.
func TestEncodeDeterministic(t *testing.T) {
	buf := bytes.Repeat([]byte("abcabcabcxyzxyz"), 100)
	a, err := Encode(buf, "9e")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encode(buf, "9e")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Errorf("nondeterministic: run1=%d bytes, run2=%d bytes", len(a), len(b))
	}
}

// TestRoundTripVMLinux: a round trip on a real vmlinux, where one is present.
// Skipped under -short. The compression ratio is logged too.
//
// TestRoundTripVMLinux - 실제 vmlinux 로 왕복한다 (있으면). -short 에서
// 스킵한다. 압축률도 로그로 남긴다.
func TestRoundTripVMLinux(t *testing.T) {
	if testing.Short() {
		t.Skip("skipped in -short mode")
	}
	path := filepath.Join("..", "..", "work", "dsm", "vmlinux")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("no fixture at %s", path)
	}
	enc, err := Encode(raw, "9e")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	dec, err := Decode(enc)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(raw, dec) {
		t.Fatalf("round trip mismatch: raw=%d, dec=%d", len(raw), len(dec))
	}
	t.Logf("vmlinux %d -> %d bytes (ratio %.3f)",
		len(raw), len(enc), float64(len(enc))/float64(len(raw)))
}

// roundTrip encodes, decodes and checks the result matches the original.
// roundTrip - Encode -> Decode 왕복 후 원본 일치를 확인한다.
func roundTrip(t *testing.T, raw []byte) {
	t.Helper()
	enc, err := Encode(raw, "9e")
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	dec, err := Decode(enc)
	if err != nil {
		t.Fatalf("Decode: %v (%d encoded bytes)", err, len(enc))
	}
	if !bytes.Equal(raw, dec) {
		t.Fatalf("round trip mismatch: raw=%d bytes, dec=%d bytes", len(raw), len(dec))
	}
}
