// kdbg looks at how kallsyms_names is laid out, anchored on the plain-text
// "do_mount" string that a 5.10 kernel carries.
//
// It is scaffolding left over from the attempt to find the mount path inside
// the kernel by string anchor. That line of work is closed - MS_MOVE turned out
// to work as it stands and needs no kernel patch - so nothing in the loader
// calls this. It is kept because the next question about kallsyms layout would
// otherwise start from nothing.
//
// kdbg - 5.10 커널에 평문으로 들어 있는 "do_mount" 문자열을 앵커 삼아
// kallsyms_names 구조를 들여다본다.
//
// 커널 안의 mount 경로를 문자열 앵커로 찾아보려던 시도의 잔재다. 그 줄기는
// 닫혔다 - MS_MOVE 는 그대로 동작하고 커널 패치가 필요 없다. 그래서 로더
// 어디서도 이걸 부르지 않는다. kallsyms 배치를 다시 물어야 할 때 맨바닥에서
// 시작하지 않으려고 남겨 둔다.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"

	"vibeldr/internal/kpatch"
)

// main takes a zImage path and dumps what surrounds each "do_mount" occurrence.
// main - zImage 경로를 받아 "do_mount" 가 나오는 자리마다 주변을 덤프한다.
func main() {
	raw, _ := os.ReadFile(os.Args[1])
	bz, _ := kpatch.ParseBzImage(raw)
	data, _ := bz.ExtractVMLinux()
	fmt.Printf("vmlinux %d bytes\n", len(data))

	// A name inside kallsyms_names is [len][type letter][tokens...]. With
	// plain-text tokens the bytes before "do_mount" carry its length. Several
	// positions are looked at.
	//
	// kallsyms_names 안에서 이름은 [len][type글자][토큰...]. 평문 토큰이면
	// "do_mount" 앞에 그 길이 관련 바이트가 온다. 여러 위치를 본다.
	needle := []byte("do_mount\x00")
	for start := 0; ; {
		i := bytes.Index(data[start:], needle)
		if i < 0 {
			break
		}
		pos := start + i
		start = pos + 1
		// A hex dump of the 16 bytes before it.
		// 앞 16바이트 hex 덤프.
		lo := pos - 16
		if lo < 0 {
			lo = 0
		}
		fmt.Printf("do_mount@0x%x  앞: % x  |  뒤2: % x\n", pos, data[lo:pos], data[pos+8:pos+12])
	}

	// token_table is many short tokens: the common prefixes "0","1",.."a".."z"
	// plus common substrings. The first tokens are often single characters, so
	// this looks for the densest run of 0x00, one character, 0x00.
	//
	// token_table 은 짧은 토큰 다수. 흔한 접두 "0","1",.."a".."z" + 흔한
	// substring. 첫 토큰이 단일 문자인 경우가 많다. 0x00 뒤 단일문자+0x00 이
	// 연속하는 밀집 구간을 찾아본다.
	fmt.Println("--- 밀집 단문자 토큰 구간 스캔 ---")
	best, bestRun := 0, 0
	run := 0
	for i := 2; i+2 < len(data); i++ {
		if data[i-1] == 0 && data[i] >= 0x20 && data[i] < 0x7f && data[i+1] == 0 {
			run++
			if run > bestRun {
				bestRun = run
				best = i
			}
		} else {
			run = 0
		}
	}
	fmt.Printf("최장 단문자열 구간 끝~0x%x, 길이 %d\n", best, bestRun)
	_ = binary.LittleEndian
}
