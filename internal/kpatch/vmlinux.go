// Package kpatch edits the DSM kernel image (bzImage) before it boots.
//
// It opens exactly one thing: the loading of unsigned kernel modules.
//
// The kernel rejects any module not signed with its own key, with
// EKEYREJECTED, and module.sig_enforce=0 does not take effect because of a
// boot parameter lock. The drivers carried on p4 are not signed with
// Synology's key, so without opening this none of the drivers the loader
// brought along will come up. There are two places that actually get touched,
// and they are described in analyze.go.
//
// The offsets are not hard-coded. Every vmlinux is analysed and the
// coordinates found afresh, so the same code works on the 4.4 line and the
// 5.10 line alike.
//
// What this package does not touch: the handover to the installed root. That
// needs no kernel patch at all - MS_MOVE works as it stands, and
// cmd/vibeldr-init/pivot_linux.go does the move directly.
//
// vmlinux sits inside the bzImage, LZMA compressed. The sequence is:
//
//	bzImage -> cut out the compressed payload -> LZMA decode -> vmlinux ELF
//	        -> analyse and find the patch sites -> flip the bytes
//	        -> LZMA recompress -> write back into the bzImage
//
// A recompressed result that is no larger than the original is written back
// over it and the remainder zeroed. The kernel's LZMA decoder stops at the
// end-of-stream marker, so the trailing zeros are ignored.
//
// Package kpatch - DSM 커널 이미지(bzImage) 를 부팅 전에 손본다.
//
// 이 패키지가 여는 것은 하나다: 서명 없는 커널 모듈의 로드.
//
// 커널은 자기 키로 서명되지 않은 모듈을 EKEYREJECTED 로 거부하고,
// `module.sig_enforce=0` 은 부트 파라미터 잠금 때문에 반영되지 않는다.
// p4 에 실은 드라이버는 시놀로지 키로 서명돼 있지 않으므로, 이걸 열지
// 못하면 로더가 싣고 온 드라이버가 하나도 안 올라온다. 실제로 손대는
// 자리는 두 곳이고 analyze.go 에 적혀 있다.
//
// 오프셋을 하드코딩하지 않고 vmlinux 를 매번 분석해 좌표를 스스로 찾는다.
// 그래서 kernel 4.4 계열이든 5.10 계열이든 같은 코드가 통한다.
//
// 이 패키지가 손대지 않는 것: 설치된 루트로 넘기는 일. 거기엔 커널 패치가
// 아예 필요 없다 - MS_MOVE 가 그대로 되고, cmd/vibeldr-init/pivot_linux.go
// 가 직접 옮긴다.
//
// vmlinux 는 bzImage 안에 LZMA 압축돼 들어있다. 절차는 위 영문 설명의
// 도식과 같다. 재압축 결과가 원본 이하이면 자리에 그대로 덮어 쓰고 뒤를
// 0 으로 채운다. 커널 LZMA 디코더는 end-of-stream 마커에서 멈추므로 뒷쪽
// 0 은 무시된다.
package kpatch

import (
	"debug/elf"
	"fmt"
	"io"
)

// VMLinux is a parsed kernel ELF image. It converts between file offset and
// virtual address using the section table and program headers, and it keeps the
// original byte slice so bytes can be edited in place.
//
// VMLinux 는 이미 파싱된 커널 ELF 이미지다. section 표와 program header 로부터
// file offset ↔ virtual address 변환을 지원하고, 원본 바이트 슬라이스도
// 그대로 갖고 있어서 in-place 바이트 편집이 된다.
type VMLinux struct {
	// Bytes is the original being edited. Modifying this slice is the whole
	// point of the package.
	//
	// Bytes 는 편집 대상 원본. 슬라이스 자체를 수정하는 게 이 패키지의 요체다.
	Bytes []byte

	loads []ptLoad
	secs  []section
}

type ptLoad struct {
	fileOff  uint64
	vaddr    uint64
	fileSize uint64
}

type section struct {
	name    string
	fileOff uint64
	vaddr   uint64
	size    uint64
}

// ParseVMLinux takes raw ELF bytes and returns a VMLinux ready to convert
// coordinates. It works from the program headers alone when there is no section
// information.
//
// ParseVMLinux 는 raw ELF 바이트를 받아 좌표 변환 준비까지 마친 VMLinux 를
// 돌려준다. section 정보가 없으면 program header 만으로도 동작한다.
func ParseVMLinux(data []byte) (*VMLinux, error) {
	f, err := elf.NewFile(newReadAt(data))
	if err != nil {
		return nil, fmt.Errorf("vmlinux ELF parse: %w", err)
	}

	v := &VMLinux{Bytes: data}
	for _, p := range f.Progs {
		if p.Type == elf.PT_LOAD && p.Filesz > 0 {
			v.loads = append(v.loads, ptLoad{
				fileOff:  p.Off,
				vaddr:    p.Vaddr,
				fileSize: p.Filesz,
			})
		}
	}
	if len(v.loads) == 0 {
		return nil, fmt.Errorf("vmlinux: no PT_LOAD segments")
	}
	for _, s := range f.Sections {
		if s.Size == 0 {
			continue
		}
		v.secs = append(v.secs, section{
			name:    s.Name,
			fileOff: s.Offset,
			vaddr:   s.Addr,
			size:    s.Size,
		})
	}
	return v, nil
}

// FileToVA converts a file offset to a virtual address. An unmapped position
// gives ok=false.
//
// FileToVA 는 파일 오프셋을 가상 주소로 변환한다. 매핑 안 되는 위치이면
// ok=false 다.
func (v *VMLinux) FileToVA(fo uint64) (uint64, bool) {
	for _, l := range v.loads {
		if fo >= l.fileOff && fo < l.fileOff+l.fileSize {
			return l.vaddr + (fo - l.fileOff), true
		}
	}
	return 0, false
}

// FindBytes returns the first file offset where needle appears, or (-1, false).
// FindBytes 는 needle 이 처음 나오는 파일 오프셋을 돌려준다. 없으면 (-1, false).
func (v *VMLinux) FindBytes(needle []byte) (int, bool) {
	i := indexBytes(v.Bytes, needle, 0)
	return i, i >= 0
}

// FindAllBytes returns every file offset where needle appears.
// FindAllBytes 는 needle 이 나오는 모든 파일 오프셋을 돌려준다.
func (v *VMLinux) FindAllBytes(needle []byte) []int {
	var out []int
	start := 0
	for {
		i := indexBytes(v.Bytes, needle, start)
		if i < 0 {
			return out
		}
		out = append(out, i)
		start = i + 1
	}
}

// TextSpans returns the (file offset, size, vaddr) of every span holding code:
// the executable sections such as .text and .init.text.
//
// TextSpans 는 코드가 있는 (file offset, size, vaddr) 목록을 돌려준다.
// .text 와 .init.text 등 실행 가능한 섹션이 대상이다.
func (v *VMLinux) TextSpans() []section {
	var out []section
	for _, s := range v.secs {
		if s.name == ".text" || s.name == ".init.text" {
			out = append(out, s)
		}
	}
	return out
}

// -----------------------------------------------------------------------------
// small helpers / 작은 헬퍼들

// indexBytes is bytes.Index with a start position. The search target is a large
// vmlinux and it is called repeatedly, so it is broken out here.
//
// indexBytes 는 bytes.Index 와 동일하지만 시작 위치를 지정할 수 있다.
// 검색 대상이 큰 vmlinux 라 반복 호출이 잦아서 별도로 뽑았다.
func indexBytes(data, needle []byte, start int) int {
	if start >= len(data) {
		return -1
	}
	if len(needle) == 0 {
		return start
	}
	for i := start; i+len(needle) <= len(data); i++ {
		match := true
		for j := 0; j < len(needle); j++ {
			if data[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// newReadAt makes the ReaderAt debug/elf wants out of a slice. bytes.NewReader
// would do, but only io.ReaderAt is needed, so this is a thin wrapper.
//
// newReadAt 는 debug/elf 가 요구하는 ReaderAt 를 슬라이스에서 만들어준다.
// bytes.NewReader 를 쓰면 되지만 io.ReaderAt 만 필요해서 얇게 래핑한다.
func newReadAt(b []byte) io.ReaderAt {
	return &sliceReader{b: b}
}

type sliceReader struct{ b []byte }

func (r *sliceReader) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r.b)) {
		return 0, io.EOF
	}
	n := copy(p, r.b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
