// kstr - prints every printable string in a kernel's vmlinux that contains a
// given substring. Used to find out what a patch anchor actually looks like in
// a real kernel before writing an analyzer against it.
//
// kstr - 커널 vmlinux 안에서 주어진 문자열을 담은 인쇄 가능 문자열을 전부
// 찍는다. 패치 앵커가 실제 커널에 어떤 형태로 들어 있는지, 분석기를 쓰기
// 전에 확인하는 용도.
package main

import (
	"bytes"
	"fmt"
	"os"
	"regexp"

	"vibeldr/internal/kpatch"
)

// main takes a zImage and a substring and prints the unique matching strings.
// main - zImage 와 부분 문자열을 받아 겹치지 않는 일치 문자열을 찍는다.
func main() {
	if len(os.Args) < 3 {
		fmt.Println("usage: kstr <zImage> <substr>")
		os.Exit(1)
	}
	raw, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	bz, err := kpatch.ParseBzImage(raw)
	if err != nil {
		panic(err)
	}
	vm, err := bz.ExtractVMLinux()
	if err != nil {
		panic(err)
	}
	sub := []byte(os.Args[2])
	re := regexp.MustCompile(`[\x20-\x7e]{4,}`)
	seen := map[string]bool{}
	for _, tok := range re.FindAll(vm, -1) {
		if bytes.Contains(tok, sub) && !seen[string(tok)] {
			seen[string(tok)] = true
			fmt.Printf("  %q\n", string(tok))
		}
	}
	fmt.Printf("총 %d 개 고유 토큰\n", len(seen))
}
