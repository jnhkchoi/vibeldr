package lzma

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

// sample is 40 copies of a pangram followed by every byte value, compressed as
// an "alone" stream with an unknown size and an end marker. It exercises
// literals, repeated distances, long matches and the marker path.
//
// sample 은 팬그램 40 번 뒤에 모든 바이트 값을 붙여, 크기 미상 + end marker 인
// "alone" 스트림으로 압축한 것이다. 리터럴, 반복 distance, 긴 매치, marker 경로를
// 모두 거친다.
const sample = "XQAAgAD//////////wAqGgiiAyVm8Ut4xaIF/y7m2dIgGq00+OId6EE2+twGabs85BA0Jwnr" +
	"s2bj7TeY7ZKt1SdFCDBeXXEdseJaD1lym5tO5hIeHelEFqDAulZuNPq/a77Hzc0cUZ2MVh9IVm+KbfNk" +
	"TvJ/DfZwZEx8LSAIsYlbN9iubr0tme4A5MO48wxFq5bZNXgl+QZ+jMcZLTQSkkcMWO/1lze48uG8/2qd" +
	"+a+IS8ZLE86jtmK2+6IWIua4H2+7HBnYBrgI9ozXXBZHi8KhBZGyQNNzLF9aOAG93AijGcOJVcmMcZES" +
	"L2zj8Tvd6tFfCpdjJrMkoGKgz+/vOEk2xoDGPN0YYaWznbEqtINFQ1Kck9UW+8ogMwTUm4R6yHUOHuyJ" +
	"Slq8ZyJ//7WGAAA="

const (
	sampleSize = 2056
	sampleHash = "7fc95a134d246599e0add5aa0687d991708094880d4d9efb00a68245c382a3ec"
)

func sampleBytes(t *testing.T) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(sample)
	if err != nil {
		t.Fatalf("decode sample: %v", err)
	}
	return b
}

func TestParseHeader(t *testing.T) {
	// 0x5d is the value every Synology stream carries: lc=3, lp=0, pb=2.
	// 0x5d 는 시놀로지 스트림이 모두 쓰는 값이다: lc=3, lp=0, pb=2.
	h, err := ParseHeader([]byte{0x5d, 0, 0, 0, 4, 0x00, 0x1c, 0xaf, 1, 0, 0, 0, 0})
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if h.LC != 3 || h.LP != 0 || h.PB != 2 {
		t.Errorf("got lc=%d lp=%d pb=%d, want 3/0/2", h.LC, h.LP, h.PB)
	}
	if h.DictSize != 64<<20 {
		t.Errorf("dict = %d, want %d", h.DictSize, 64<<20)
	}
	if !h.SizeKnown || h.Size != 28253184 {
		t.Errorf("size = %d (known=%v), want 28253184", h.Size, h.SizeKnown)
	}
}

func TestParseHeaderUnknownSize(t *testing.T) {
	b := append([]byte{0x5d, 0, 0, 0, 4}, bytes.Repeat([]byte{0xff}, 8)...)
	h, err := ParseHeader(b)
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if h.SizeKnown {
		t.Error("a size of all ones means unknown, but SizeKnown is true")
	}
}

func TestParseHeaderRejectsBadProperties(t *testing.T) {
	// 225 is the first value that cannot encode a valid lc/lp/pb triple.
	// 225 는 유효한 lc/lp/pb 조합을 담을 수 없는 첫 값이다.
	if _, err := ParseHeader([]byte{225, 0, 0, 0, 4, 0, 0, 0, 0, 0, 0, 0, 0}); err == nil {
		t.Error("expected an error for an out-of-range properties byte")
	}
}

func TestDecodeEndMarkerStream(t *testing.T) {
	got, err := Decode(sampleBytes(t))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if len(got) != sampleSize {
		t.Fatalf("got %d bytes, want %d", len(got), sampleSize)
	}
	sum := sha256.Sum256(got)
	if h := hex.EncodeToString(sum[:]); h != sampleHash {
		t.Fatalf("sha256 = %s, want %s", h, sampleHash)
	}
}

// TestDecodeDeclaredSizeStream covers the other half of the format: a stream
// whose length is in the header. Synology writes these, and they are the ones
// that trip up readers expecting a marker.
//
// TestDecodeDeclaredSizeStream 은 포맷의 나머지 절반, 곧 길이가 헤더에 있는
// 스트림을 다룬다. 시놀로지가 이 형태로 쓰고, marker 를 기대하는 리더가 걸려
// 넘어지는 것도 이쪽이다.
func TestDecodeDeclaredSizeStream(t *testing.T) {
	b := sampleBytes(t)
	binary.LittleEndian.PutUint64(b[5:13], sampleSize)

	got, err := Decode(b)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	sum := sha256.Sum256(got)
	if h := hex.EncodeToString(sum[:]); h != sampleHash {
		t.Fatalf("sha256 = %s, want %s", h, sampleHash)
	}
}

func TestDecodeReportsTruncation(t *testing.T) {
	b := sampleBytes(t)
	binary.LittleEndian.PutUint64(b[5:13], sampleSize)

	_, err := Decode(b[:len(b)/2])
	if err == nil {
		t.Fatal("expected an error for a truncated stream")
	}
}

func TestDecodeRejectsNonZeroFirstByte(t *testing.T) {
	b := sampleBytes(t)
	b[HeaderSize] = 1
	if _, err := Decode(b); err == nil {
		t.Error("expected an error when the range coder padding byte is not zero")
	}
}

func TestDecodeRejectsShortInput(t *testing.T) {
	if _, err := Decode([]byte{0x5d, 0, 0}); err == nil {
		t.Error("expected an error for input shorter than a header")
	}
}

// TestDecodeRealRamdisk is the test that actually matters: Synology's own
// rd.gz, compared byte for byte against a reference extraction. It is skipped
// when the work directory is not populated.
//
// TestDecodeRealRamdisk 가 정말 중요한 테스트다. 시놀로지 원본 rd.gz 를 참조
// 추출본과 바이트 단위로 비교한다. work 디렉터리가 채워져 있지 않으면 건너뛴다.
func TestDecodeRealRamdisk(t *testing.T) {
	root := filepath.Join("..", "..", "work", "dsm-real")
	src, err := os.ReadFile(filepath.Join(root, "rd.gz"))
	if err != nil {
		t.Skip("work/dsm-real/rd.gz is not present")
	}
	want, err := os.ReadFile(filepath.Join(root, "rd.cpio"))
	if err != nil {
		t.Skip("work/dsm-real/rd.cpio is not present")
	}

	got, err := Decode(src)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded %d bytes, reference is %d, contents differ", len(got), len(want))
	}
}
