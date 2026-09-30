// kdbg looks at how kallsyms_names is laid out, anchored on the plain-text
// "do_mount" string that a 5.10 kernel carries.
//
// It is a standalone developer tool for questions about the kallsyms layout;
// nothing in the loader calls it.
//
// kdbg - 5.10 커널에 평문으로 들어 있는 "do_mount" 문자열을 앵커 삼아
// kallsyms_names 구조를 들여다본다.
//
// kallsyms 배치를 살펴볼 때 쓰는 독립 개발 도구다. 로더 어디서도 이걸
// 부르지 않는다.
package main

import (
	"bytes"
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
		fmt.Printf("do_mount@0x%x  before: % x  |  after2: % x\n", pos, data[lo:pos], data[pos+8:pos+12])
	}

	// token_table is many short tokens: the common prefixes "0","1",.."a".."z"
	// plus common substrings. The first tokens are often single characters, so
	// this looks for the densest run of 0x00, one character, 0x00.
	//
	// token_table 은 짧은 토큰 다수. 흔한 접두 "0","1",.."a".."z" + 흔한
	// substring. 첫 토큰이 단일 문자인 경우가 많다. 0x00 뒤 단일문자+0x00 이
	// 연속하는 밀집 구간을 찾아본다.
	fmt.Println("--- scanning for dense single-character token runs ---")
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
	fmt.Printf("longest single-character run ends ~0x%x, length %d\n", best, bestRun)
}
