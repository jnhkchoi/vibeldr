// Package ramdisk reads and writes the initramfs archive DSM ships.
//
// DSM's rd.gz is an LZMA-compressed cpio "newc" archive, and everything the
// loader has to change is inside it: etc/model.dtb describes where the disks
// are, and linuxrc.syno.impl is where our code slots in before DSM goes
// looking for them.
//
// Package ramdisk - DSM 이 배포하는 initramfs 아카이브 읽기/쓰기.
//
// DSM 의 rd.gz 는 LZMA 압축된 cpio "newc" 아카이브다. 로더가 손봐야 하는
// 건 다 그 안에 있다. etc/model.dtb 가 디스크 위치를 서술하고,
// linuxrc.syno.impl 이 DSM 이 디스크 찾기 전에 우리 코드가 낄 자리다.
package ramdisk

import (
	"bytes"
	"fmt"
	"io"
	"sort"
	"strings"
)

const (
	cpioMagic      = "070701"
	cpioHeaderSize = 110
	cpioTrailer    = "TRAILER!!!"
)

// The file mode stored in a cpio header.
// cpio 헤더에 저장되는 파일 모드.
const (
	ModeFileTypeMask = 0o170000
	ModeRegular      = 0o100000
	ModeDirectory    = 0o040000
	ModeSymlink      = 0o120000
)

// Entry is one item in the archive.
// Entry - 아카이브 안의 항목 하나.
type Entry struct {
	Name string
	Mode uint32
	UID  uint32
	GID  uint32
	// Mtime is kept so a repack does not needlessly change every timestamp.
	// Mtime - 재패킹이 모든 타임스탬프를 쓸데없이 바꾸지 않도록 보존한다.
	Mtime uint32
	Data  []byte

	// Ino, Nlink, DevMajor and friends are preserved verbatim, because hard
	// links in the archive are expressed through matching inode numbers.
	//
	// Ino, Nlink, DevMajor 등은 원문 그대로 보존한다. 아카이브 안의
	// 하드링크가 같은 inode 번호로 표현되기 때문이다.
	Ino       uint32
	Nlink     uint32
	DevMajor  uint32
	DevMinor  uint32
	RdevMajor uint32
	RdevMinor uint32
}

// IsRegular reports whether this is a regular file.
// IsRegular - 일반 파일 여부.
func (e Entry) IsRegular() bool { return e.Mode&ModeFileTypeMask == ModeRegular }

// IsDir reports whether this is a directory.
// IsDir - 디렉터리 여부.
func (e Entry) IsDir() bool { return e.Mode&ModeFileTypeMask == ModeDirectory }

// Archive is a parsed cpio archive, in its original order.
//
// The order has to be preserved because an initramfs is unpacked
// sequentially: a file cannot appear before the directory that holds it.
//
// Archive - 파싱된 cpio 아카이브. 원본 순서를 유지한다.
//
// initramfs 는 순차적으로 언팩되기 때문에 순서 보존이 필요하다. 파일은
// 자기가 담긴 디렉터리보다 먼저 나올 수 없다.
type Archive struct {
	Entries []Entry
	index   map[string]int
}

// NewArchive starts an empty archive.
//
// It is for archives the loader builds from nothing, such as the early-microcode
// images, the driver pack written to partition 4, the scemd cache bundle and
// the loader's own boot ramdisk. A DSM ramdisk is not built this way: it is
// read with ReadCPIO and edited.
//
// NewArchive - 빈 아카이브 시작.
//
// 로더가 무에서부터 만드는 아카이브에 쓴다. 예를 들어 조기 마이크로코드 이미지,
// 파티션 4 에 쓰는 드라이버 팩, scemd 캐시 묶음, 로더 자신의 부팅 램디스크가
// 그렇다. DSM 램디스크는 이렇게 만들지 않고 ReadCPIO 로 읽어 편집한다.
func NewArchive() *Archive {
	return &Archive{index: make(map[string]int)}
}

// ReadCPIO parses a newc-format archive.
// ReadCPIO - newc 형식 아카이브 파싱.
func ReadCPIO(data []byte) (*Archive, error) { return readCPIO(data, true) }

// ReadCPIOShared parses a newc-format archive without copying the file
// bodies: each entry's Data is a window into data, which the caller must then
// neither change nor reuse. It is for the driver pack, a few hundred MB read
// in a ramdisk where every copy costs that much RAM.
//
// ReadCPIOShared - 파일 본문을 복사하지 않고 newc 아카이브를 파싱한다. 각 항목의
// Data 는 data 를 들여다보는 창이라, 호출자는 그 버퍼를 바꾸거나 다시 쓰면 안 된다.
// 드라이버 팩용이다 - 램디스크에서 수백 MB 를 읽으니 사본 하나가 그만큼의 RAM 이다.
func ReadCPIOShared(data []byte) (*Archive, error) { return readCPIO(data, false) }

func readCPIO(data []byte, copyData bool) (*Archive, error) {
	a := &Archive{index: make(map[string]int)}

	off := 0
	for off+cpioHeaderSize <= len(data) {
		if string(data[off:off+6]) != cpioMagic {
			return nil, fmt.Errorf("bad cpio magic at offset %d: %q", off, data[off:off+6])
		}
		field := func(i int) (uint32, error) {
			start := off + 6 + i*8
			var v uint32
			if _, err := fmt.Sscanf(string(data[start:start+8]), "%08x", &v); err != nil {
				return 0, fmt.Errorf("bad header field %d at offset %d: %w", i, off, err)
			}
			return v, nil
		}

		var f [13]uint32
		for i := 0; i < 13; i++ {
			v, err := field(i)
			if err != nil {
				return nil, err
			}
			f[i] = v
		}
		// Field order: ino mode uid gid nlink mtime filesize devmajor devminor
		// rdevmajor rdevminor namesize check.
		//
		// 필드 순서: ino mode uid gid nlink mtime filesize devmajor devminor
		// rdevmajor rdevminor namesize check.
		fileSize, nameSize := f[6], f[11]

		nameStart := off + cpioHeaderSize
		if nameStart+int(nameSize) > len(data) {
			return nil, fmt.Errorf("truncated name at offset %d", off)
		}
		name := string(data[nameStart : nameStart+int(nameSize)-1]) // drop the NUL / NUL 제외

		dataStart := align4(nameStart + int(nameSize))
		if dataStart+int(fileSize) > len(data) {
			return nil, fmt.Errorf("truncated data for %q", name)
		}
		body := data[dataStart : dataStart+int(fileSize)]
		off = align4(dataStart + int(fileSize))

		if name == cpioTrailer {
			return a, nil
		}

		// The body is a window into the caller's buffer; ReadCPIO copies it so
		// the archive stays valid if that buffer is reused.
		//
		// 본문은 호출자 버퍼를 들여다보는 창이다. ReadCPIO 는 그 버퍼가 재사용돼도
		// 아카이브가 유효하게 남도록 복사해 둔다.
		if copyData {
			body = append([]byte(nil), body...)
		}
		a.index[name] = len(a.Entries)
		a.Entries = append(a.Entries, Entry{
			Name: name, Ino: f[0], Mode: f[1], UID: f[2], GID: f[3],
			Nlink: f[4], Mtime: f[5],
			DevMajor: f[7], DevMinor: f[8], RdevMajor: f[9], RdevMinor: f[10],
			Data: body,
		})
	}
	return nil, fmt.Errorf("cpio archive ended without a %s record", cpioTrailer)
}

func align4(n int) int { return (n + 3) &^ 3 }

// Get looks an entry up by name.
// Get - 이름으로 항목 조회.
func (a *Archive) Get(name string) (*Entry, bool) {
	i, ok := a.index[name]
	if !ok {
		return nil, false
	}
	return &a.Entries[i], true
}

// Replace overwrites an existing file's contents, keeping its metadata.
// Replace - 기존 파일 내용을 덮어쓴다. 메타데이터는 유지한다.
func (a *Archive) Replace(name string, data []byte) error {
	i, ok := a.index[name]
	if !ok {
		return fmt.Errorf("%s is not in the archive", name)
	}
	if !a.Entries[i].IsRegular() {
		return fmt.Errorf("%s is not a regular file", name)
	}
	a.Entries[i].Data = data
	return nil
}

// Add adds a new regular file. A name collision fails rather than silently
// overwriting, so a collision is always visible. Missing parent directory
// entries are created first.
//
// Add - 새 일반 파일을 추가한다. 이름 충돌 시 조용히 덮지 않고 실패해서
// 충돌이 항상 눈에 보이게 한다. 상위 디렉터리 엔트리가 없으면 먼저 만든다.
func (a *Archive) Add(name string, mode uint32, data []byte) error {
	if _, exists := a.index[name]; exists {
		return fmt.Errorf("%s is already in the archive", name)
	}
	a.addParentDirs(name)
	a.index[name] = len(a.Entries)
	a.Entries = append(a.Entries, Entry{
		Name:  name,
		Mode:  ModeRegular | (mode & 0o7777),
		Nlink: 1,
		Data:  data,
	})
	return nil
}

// addParentDirs fills in directory entries for every parent path of name.
//
// The kernel's initramfs unpacker does not create parent directories. It just
// tries to open the regular file, and when the parent path is missing it gets
// ENOENT and skips it quietly. So to place lib/modules/VER/.../x.ko, each of
// lib, lib/modules and the rest has to appear as a directory entry earlier in
// the archive. Userspace cpio creates parents on its own, which is why the
// difference never shows up there.
//
// addParentDirs - name 의 상위 경로들을 디렉터리 엔트리로 채워 넣는다.
//
// 커널 initramfs 언패커는 상위 디렉터리를 만들어 주지 않는다. 정규 파일은
// 그냥 열어보고 실패하면 (상위 경로가 없으면 ENOENT) 조용히 건너뛴다.
// 따라서 lib/modules/VER/.../x.ko 를 넣으려면 lib, lib/modules, ... 각
// 단계가 디렉터리 엔트리로 아카이브 안에 먼저 나와 있어야 한다.
// 유저스페이스 cpio 는 상위 경로를 알아서 만들기 때문에 이 차이가 드러나지
// 않는다.
func (a *Archive) addParentDirs(name string) {
	for i, c := range name {
		if c != '/' || i == 0 {
			continue
		}
		dir := name[:i]
		if _, exists := a.index[dir]; exists {
			continue
		}
		a.index[dir] = len(a.Entries)
		a.Entries = append(a.Entries, Entry{
			Name: dir,
			Mode: ModeDirectory | 0o755,
			// A directory has at least 2: its own "." plus the parent's entry.
			// 디렉터리는 자기 자신 (.) 과 부모의 엔트리를 합쳐 최소 2 다.
			Nlink: 2,
		})
	}
}

// Patch rewrites a text file in place through fn.
//
// This is how hooks are inserted into DSM's boot scripts: read the script,
// transform it, write it back. Expressing the transform as a function lets the
// caller check the result before it is committed.
//
// Patch - fn 을 통해 텍스트 파일을 in-place 로 재작성한다.
//
// DSM 부팅 스크립트에 훅을 삽입하는 방식이 이것이다. 스크립트를 읽고
// 변형해서 다시 쓴다. 변형이 함수로 표현되므로 커밋 전에 호출자가 결과를
// 검증할 수 있다.
func (a *Archive) Patch(name string, fn func(string) (string, error)) error {
	e, ok := a.Get(name)
	if !ok {
		return fmt.Errorf("%s is not in the archive", name)
	}
	out, err := fn(string(e.Data))
	if err != nil {
		return fmt.Errorf("patch %s: %w", name, err)
	}
	e.Data = []byte(out)
	return nil
}

// Names returns every entry name, sorted. For diagnostics.
// Names - 모든 항목 이름을 정렬해서 돌려준다. 진단용.
func (a *Archive) Names() []string {
	out := make([]string, 0, len(a.Entries))
	for _, e := range a.Entries {
		out = append(out, e.Name)
	}
	sort.Strings(out)
	return out
}

// WriteCPIO serialises the archive in newc format.
//
// Each entry is given a unique ino. The kernel's initramfs unpacker treats a
// shared ino as a hard link, so leaving them all at 0 would unpack the first
// entry and skip the rest. An Ino the caller set to something non-zero is
// written as given.
//
// WriteCPIO - 아카이브를 newc 형식으로 직렬화해 쓴다.
//
// 각 엔트리에 유니크한 ino 를 부여한다. 커널 initramfs 언패커는 같은 ino 를
// 하드링크로 간주하므로 모두 0 이면 첫 항목만 풀고 나머지는 스킵된다.
// 호출자가 Entry.Ino 를 0 이 아닌 값으로 지정했으면 그 값을 그대로 쓴다.
func (a *Archive) WriteCPIO(w io.Writer) error {
	cw := &countingWriter{w: w}

	var nextIno uint32 = 1
	for _, e := range a.Entries {
		if e.Ino == 0 {
			e.Ino = nextIno
			nextIno++
		}
		if err := writeEntry(cw, e); err != nil {
			return err
		}
	}
	// The trailer marks the end of the archive; without it the kernel keeps
	// reading into whatever follows.
	//
	// 트레일러가 아카이브의 끝을 표시한다. 없으면 커널이 그 뒤에 무엇이
	// 있든 계속 읽어 들인다.
	if err := writeEntry(cw, Entry{Name: cpioTrailer, Nlink: 1}); err != nil {
		return err
	}
	// Archives are padded to a 512 byte boundary.
	// 아카이브는 512 바이트 경계까지 패딩한다.
	if pad := (512 - cw.n%512) % 512; pad > 0 {
		if _, err := cw.Write(make([]byte, pad)); err != nil {
			return err
		}
	}
	return nil
}

// Bytes serialises the archive into memory.
// Bytes - 아카이브를 메모리 바이트로 직렬화한다.
func (a *Archive) Bytes() ([]byte, error) {
	var buf bytes.Buffer
	if err := a.WriteCPIO(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

type countingWriter struct {
	w io.Writer
	n int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}

func writeEntry(w *countingWriter, e Entry) error {
	name := e.Name + "\x00"
	mode := e.Mode
	if e.Name == cpioTrailer {
		mode = 0
	}
	nlink := e.Nlink
	if nlink == 0 {
		nlink = 1
	}

	var hdr strings.Builder
	hdr.WriteString(cpioMagic)
	for _, v := range []uint32{
		e.Ino, mode, e.UID, e.GID, nlink, e.Mtime,
		uint32(len(e.Data)), e.DevMajor, e.DevMinor,
		e.RdevMajor, e.RdevMinor, uint32(len(name)), 0,
	} {
		fmt.Fprintf(&hdr, "%08X", v)
	}
	if _, err := w.Write([]byte(hdr.String())); err != nil {
		return err
	}
	if _, err := w.Write([]byte(name)); err != nil {
		return err
	}
	if err := pad4(w); err != nil {
		return err
	}
	if len(e.Data) > 0 {
		if _, err := w.Write(e.Data); err != nil {
			return err
		}
		if err := pad4(w); err != nil {
			return err
		}
	}
	return nil
}

func pad4(w *countingWriter) error {
	if pad := (4 - w.n%4) % 4; pad > 0 {
		_, err := w.Write(make([]byte, pad))
		return err
	}
	return nil
}
