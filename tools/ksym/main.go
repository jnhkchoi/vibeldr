// ksym checks whether a vmlinux ELF still carries a symbol table, and whether
// kallsyms is there, by listing the sections and looking for a handful of mount
// functions by name.
//
// It is scaffolding left over from the attempt to reach the mount path through
// the kernel's own symbols. That line of work is closed - MS_MOVE turned out to
// work as it stands - so the loader does not use this.
//
// ksym - vmlinux ELF 에 심볼 테이블이 남아 있는지, kallsyms 가 있는지 본다.
// 섹션을 나열하고 mount 관련 함수 몇 개를 이름으로 찾아본다.
//
// 커널 자체 심볼로 mount 경로에 닿아 보려던 시도의 잔재다. 그 줄기는 닫혔고
// (MS_MOVE 는 그대로 동작한다) 로더는 이걸 쓰지 않는다.
package main

import (
	"debug/elf"
	"fmt"
	"os"

	"vibeldr/internal/kpatch"
)

// main takes a zImage path, writes the vmlinux out for other tools to look at,
// and reports what the ELF holds.
//
// main - zImage 경로를 받아 vmlinux 를 따로 써 두고 (다른 도구가 보게)
// ELF 가 무엇을 담고 있는지 알려 준다.
func main() {
	raw, _ := os.ReadFile(os.Args[1])
	bz, err := kpatch.ParseBzImage(raw)
	if err != nil {
		panic(err)
	}
	vm, err := bz.ExtractVMLinux()
	if err != nil {
		panic(err)
	}
	os.WriteFile("/tmp/vmlinux.elf", vm, 0644)

	f, err := elf.NewFile(bytesReaderAt(vm))
	if err != nil {
		fmt.Println("ELF 파싱 실패:", err)
		return
	}
	fmt.Println("=== 섹션 ===")
	for _, s := range f.Sections {
		fmt.Printf("  %-24s type=%v size=%d\n", s.Name, s.Type, s.Size)
	}
	syms, err := f.Symbols()
	if err != nil {
		fmt.Println("Symbols():", err)
	} else {
		fmt.Printf("=== .symtab 심볼 %d 개 ===\n", len(syms))
		for _, want := range []string{"check_mnt", "do_mount", "graft_tree", "attach_recursive_mnt", "do_move_mount", "SyS_mount", "sys_mount"} {
			for _, s := range syms {
				if s.Name == want {
					fmt.Printf("  %-24s value=0x%x size=%d\n", s.Name, s.Value, s.Size)
					break
				}
			}
		}
	}
}

// raBytes makes a byte slice readable by debug/elf, which wants an io.ReaderAt.
// raBytes - 바이트 슬라이스를 debug/elf 가 요구하는 io.ReaderAt 로 만든다.
type raBytes []byte

// ReadAt is the io.ReaderAt half of raBytes.
// ReadAt - raBytes 의 io.ReaderAt 구현.
func (b raBytes) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, fmt.Errorf("eof")
	}
	n := copy(p, b[off:])
	return n, nil
}

// bytesReaderAt is the conversion, named so the call site reads clearly.
// bytesReaderAt - 변환 함수. 호출부가 읽기 쉬우라고 이름을 붙였다.
func bytesReaderAt(b []byte) raBytes { return raBytes(b) }
