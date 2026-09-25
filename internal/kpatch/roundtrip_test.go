package kpatch

import (
	"os"
	"testing"
)

// TestFullRoundtrip takes the zImage-dsm the patch pipeline produced, decodes
// it again with our own decoder and checks that every code patch site holds
// its patched byte (Data). The coordinates to check come from analysing the
// original zImage again, since the same coordinates differ per kernel.
//
// With either file missing - a CI environment where the pipeline has not been
// run, say - it skips.
//
// TestFullRoundtrip 는 patch 파이프라인이 만들어 놓은 zImage-dsm 을 다시
// 우리 디코더로 풀어서 코드 패치 사이트마다 패치된 바이트 (Data) 가 들어
// 있는지 검증한다. 검증할 좌표는 원본 zImage 를 다시 분석해서 얻는다 (같은
// 좌표가 커널마다 다르므로).
//
// 두 파일 중 하나라도 없으면 (예: 아직 파이프라인을 안 돌린 CI 환경)
// skip 한다.
func TestFullRoundtrip(t *testing.T) {
	origRaw, err := os.ReadFile("../../work/dsm/zImage")
	if err != nil {
		t.Skip("work/dsm/zImage 이 없음")
	}
	patRaw, err := os.ReadFile("../../work/dsm/zImage-dsm")
	if err != nil {
		t.Skip("work/dsm/zImage-dsm 이 없음 (vibeldr patch 먼저)")
	}

	origVM := decompress(t, origRaw)
	patVM := decompress(t, patRaw)

	vm, err := ParseVMLinux(origVM)
	if err != nil {
		t.Fatalf("ParseVMLinux(orig): %v", err)
	}
	f, err := Analyze(vm)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	// Whether every code-mode site - the boot parameter lock ANDs and the
	// ramdisk-check JMP - really holds its patched byte in the patched copy.
	//
	// 코드 모드 사이트 (부트 파라미터 잠금의 AND, 램디스크 검사의 JMP) 가 패치본에서
	// 실제로 패치된 바이트를 갖는지 본다.
	var checked int
	for i := range f.Sites {
		s := &f.Sites[i]
		if !s.CodeMode || len(s.Data) == 0 {
			continue
		}
		if len(patVM) <= int(s.FileOff) {
			t.Fatalf("patched vmlinux 가 예상보다 짧음 (%d bytes, need > %d)", len(patVM), s.FileOff)
		}
		if got := patVM[s.FileOff]; got != s.Data[0] {
			t.Errorf("%s 좌표 0x%x: 예상 0x%02x, 실제 0x%02x", s.Name, s.FileOff, s.Data[0], got)
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("검증할 코드 패치 사이트를 원본 vmlinux 에서 못 찾음")
	}
}

func decompress(t *testing.T, raw []byte) []byte {
	t.Helper()
	bz, err := ParseBzImage(raw)
	if err != nil {
		t.Fatalf("ParseBzImage: %v", err)
	}
	v, err := bz.ExtractVMLinux()
	if err != nil {
		t.Fatalf("ExtractVMLinux: %v", err)
	}
	return v
}
