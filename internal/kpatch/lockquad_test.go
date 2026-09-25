package kpatch

import "testing"

// TestMatchLockQuadDeterministic: where several complete quad candidates exist
// - the real one packed inside a single function, a false one scattered across
// the kernel - matchLockQuad has to pick the packed one every time,
// deterministically. The candidates gather in a map and map iteration has no
// order, so without sorting a different one is picked on each run. Picking the
// false one flips an unrelated piece of code's OR into an AND and breaks the
// DS918+ boot.
//
// TestMatchLockQuadDeterministic - matchLockQuad 는 완전한 quad 후보가 여럿일 때
// (진짜 = 한 함수 안에 뭉친 것, 가짜 = 커널 전역에 흩어진 것) 항상 뭉친 쪽을
// 결정론적으로 골라야 한다. 후보는 map 에 모이고 map 순회는 순서가 무작위라,
// 정렬하지 않으면 실행마다 다른 쪽이 잡힌다. 가짜를 고르면 무관한 코드의
// OR 을 AND 로 뒤집어 DS918+ 부팅이 깨진다.
func TestMatchLockQuadDeterministic(t *testing.T) {
	v := &VMLinux{Bytes: make([]byte, 0x40000)}
	// The F0 80 0D <disp32> <imm8> form is planted and the lockOr list built
	// directly, rather than going through scanLockOrRIP, so that matchLockQuad
	// alone is under test.
	//
	// F0 80 0D <disp32> <imm8> 형태를 심어 scanLockOrRIP 가 아니라 직접
	// lockOr 리스트를 구성해 matchLockQuad 만 검증한다.
	tight := []lockOr{
		{fileOff: 0x1000, imm: 1, target: 0xAAAA},
		{fileOff: 0x1010, imm: 2, target: 0xAAAA},
		{fileOff: 0x1020, imm: 4, target: 0xAAAA},
		{fileOff: 0x1030, imm: 8, target: 0xAAAA}, // span 0x30
	}
	scattered := []lockOr{
		{fileOff: 0x02000, imm: 1, target: 0xBBBB},
		{fileOff: 0x12000, imm: 2, target: 0xBBBB},
		{fileOff: 0x22000, imm: 4, target: 0xBBBB},
		{fileOff: 0x32000, imm: 8, target: 0xBBBB}, // span 0x30000
	}
	// Run several times in both orders, the tight one (target 0xAAAA, or1 site
	// at 0x1002) is always the choice.
	//
	// 두 순서로 각각 여러 번 돌려도 항상 tight(대상 0xAAAA, or1 사이트 0x1002) 를
	// 고른다.
	for _, order := range [][]lockOr{
		append(append([]lockOr{}, scattered...), tight...),
		append(append([]lockOr{}, tight...), scattered...),
	} {
		for i := 0; i < 20; i++ {
			sites := matchLockQuad(v, order)
			if len(sites) != 4 {
				t.Fatalf("quad 4개 나와야 함, got %d", len(sites))
			}
			// or1's FileOff is the tight or1 fileOff plus 2, the modrm after F0 80.
			// or1 의 FileOff = tight or1 fileOff + 2 (F0 80 다음 modrm).
			if sites[0].FileOff != 0x1002 {
				t.Fatalf("뭉친 quad 를 골라야 함 (or1 @0x1002), got 0x%x", sites[0].FileOff)
			}
		}
	}
}

// TestMatchLockQuadTieBreak: on an equal span, the lowest address wins, which
// keeps it deterministic.
//
// TestMatchLockQuadTieBreak - span 이 같으면 최소 주소 우선으로 결정론적이다.
func TestMatchLockQuadTieBreak(t *testing.T) {
	v := &VMLinux{Bytes: make([]byte, 0x40000)}
	mk := func(base int, tgt uint64) []lockOr {
		return []lockOr{
			{fileOff: base, imm: 1, target: tgt},
			{fileOff: base + 0x10, imm: 2, target: tgt},
			{fileOff: base + 0x20, imm: 4, target: tgt},
			{fileOff: base + 0x30, imm: 8, target: tgt},
		}
	}
	locks := append(mk(0x5000, 0xCCCC), mk(0x1000, 0xDDDD)...) // same span 0x30 / 같은 span 0x30
	sites := matchLockQuad(v, locks)
	if len(sites) != 4 || sites[0].FileOff != 0x1002 {
		t.Fatalf("동률이면 최소 주소(0x1000→0x1002) 선택해야 함, got 0x%x", sites[0].FileOff)
	}
}
