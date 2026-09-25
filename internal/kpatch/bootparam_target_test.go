package kpatch

import (
	"os"
	"testing"
)

// TestBootParamLocksOnTargetKernel checks that the LOCK-OR quad for the boot
// parameter lock is found in a real SA6400 kernel. It only runs when the zImage
// from result.tgz is present locally.
//
// TestBootParamLocksOnTargetKernel - 실제 SA6400 커널에서 부트 파라미터 잠금
// LOCK-OR quad 를 찾는지 확인한다. result.tgz 의 zImage 가 로컬에 있을 때만
// 돈다.
func TestBootParamLocksOnTargetKernel(t *testing.T) {
	raw, err := os.ReadFile(`C:\vibeldr-tmp\result\zImage`)
	if err != nil {
		t.Skipf("타깃 zImage 없음: %v", err)
	}
	bz, err := ParseBzImage(raw)
	if err != nil {
		t.Fatalf("ParseBzImage: %v", err)
	}
	vmlinux, err := bz.ExtractVMLinux()
	if err != nil {
		t.Fatalf("ExtractVMLinux: %v", err)
	}
	v, err := ParseVMLinux(vmlinux)
	if err != nil {
		t.Fatalf("ParseVMLinux: %v", err)
	}

	sites, err := findBootParamLocks(v)
	if err != nil {
		t.Fatalf("findBootParamLocks: %v", err)
	}
	if len(sites) != 4 {
		t.Fatalf("사이트 %d 개, 4 개여야 함", len(sites))
	}
	for _, s := range sites {
		got := v.Bytes[s.FileOff]
		t.Logf("%-20s 파일오프셋 0x%x  현재 %#02x -> %#02x", s.Name, s.FileOff, got, s.New)
		if got != s.Old {
			t.Errorf("%s: 원본 바이트 %#02x, 기대 %#02x", s.Name, got, s.Old)
		}
	}

	// Check with Apply, in code mode, that all four sites really apply and each
	// leaves 0x25 (AND) behind.
	//
	// Apply 로 네 사이트가 코드 모드에서 실제로 적용되고, 각 자리에 0x25 (AND)
	// 가 남는지 확인한다.
	res := Apply(v, sites)
	if len(res.Applied) != 4 {
		t.Fatalf("적용 %d, 4 여야 함. skipped=%v", len(res.Applied), res.Skipped)
	}
	for _, s := range sites {
		if v.Bytes[s.FileOff] != 0x25 {
			t.Errorf("%s: 적용 후 %#02x, 0x25 여야 함", s.Name, v.Bytes[s.FileOff])
		}
	}
}

// TestRamdiskCheckJZOnTargetKernel checks that the JZ of the ramdisk signature
// check branch is found in a real SA6400 kernel. That site has to be there for
// unsigned module loading to actually succeed alongside the boot parameter
// unlock.
//
// TestRamdiskCheckJZOnTargetKernel - 실제 SA6400 커널에서 램디스크 서명
// 검증 분기의 JZ 를 찾는지 확인한다. 이 사이트가 있어야 부트파라미터 락
// 해제와 함께 서명 없는 모듈 로드가 실제로 성공한다.
func TestRamdiskCheckJZOnTargetKernel(t *testing.T) {
	raw, err := os.ReadFile(`C:\vibeldr-tmp\result\zImage`)
	if err != nil {
		t.Skipf("타깃 zImage 없음: %v", err)
	}
	bz, err := ParseBzImage(raw)
	if err != nil {
		t.Fatalf("ParseBzImage: %v", err)
	}
	vmlinux, err := bz.ExtractVMLinux()
	if err != nil {
		t.Fatalf("ExtractVMLinux: %v", err)
	}
	v, err := ParseVMLinux(vmlinux)
	if err != nil {
		t.Fatalf("ParseVMLinux: %v", err)
	}

	s, err := findRamdiskCheckJZ(v)
	if err != nil {
		t.Fatalf("findRamdiskCheckJZ: %v", err)
	}
	t.Logf("ramdisk_check_jz 파일오프셋 0x%x  현재 %#02x -> %#02x", s.FileOff, v.Bytes[s.FileOff], s.New)
	if got := v.Bytes[s.FileOff]; got != 0x74 {
		t.Errorf("원본 바이트 %#02x, JZ (0x74) 여야 함", got)
	}

	// Printing the surrounding context too makes it checkable by eye.
	// 앞뒤 컨텍스트도 찍어 두면 사람 눈으로도 검증할 수 있다.
	lo, hi := int(s.FileOff)-6, int(s.FileOff)+2
	if lo < 0 {
		lo = 0
	}
	if hi > len(v.Bytes) {
		hi = len(v.Bytes)
	}
	t.Logf("주변 바이트: % x", v.Bytes[lo:hi])

	res := Apply(v, []Site{s})
	if len(res.Applied) != 1 {
		t.Fatalf("적용 %d, 1 이어야 함. skipped=%v", len(res.Applied), res.Skipped)
	}
	if got := v.Bytes[s.FileOff]; got != 0xEB {
		t.Errorf("적용 후 %#02x, JMP (0xEB) 여야 함", got)
	}
}
