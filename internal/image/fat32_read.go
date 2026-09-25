package image

import (
	"encoding/binary"
	"fmt"
	"io"
	"path"
	"strings"
	"unicode/utf16"
)

// FAT32Reader reads an existing FAT32 filesystem.
//
// This exists for one reason: a donor image's GRUB core is only a loader stub.
// It expects to find the rest of GRUB - normal.mod and friends - on the boot
// partition, so those files have to travel with the boot code. Copying the
// donor's boot sectors without them lands in `grub rescue>`.
//
// FAT32Reader - 이미 있는 FAT32 파일시스템을 읽는다.
//
// 있는 이유는 하나다. 도너 이미지의 GRUB core 는 로더 조각일 뿐이라, GRUB 의
// 나머지 (normal.mod 등) 를 부트 파티션에서 찾을 것으로 기대한다. 그래서 그
// 파일들이 부트 코드와 함께 따라와야 한다. 도너의 부트 섹터만 베끼고 그것들을
// 빼면 `grub rescue>` 로 떨어진다.
type FAT32Reader struct {
	r      io.ReaderAt
	offset int64

	secPerClus uint32
	rsvdSecCnt uint32
	numFATs    uint32
	fatSize    uint32
	rootClus   uint32
	label      string
}

// OpenFAT32 parses the boot sector of the filesystem at offset.
// OpenFAT32 - offset 위치 파일시스템의 부트 섹터를 파싱한다.
func OpenFAT32(r io.ReaderAt, offset int64) (*FAT32Reader, error) {
	boot := make([]byte, SectorSize)
	if _, err := r.ReadAt(boot, offset); err != nil {
		return nil, fmt.Errorf("read boot sector: %w", err)
	}
	if boot[510] != 0x55 || boot[511] != 0xaa {
		return nil, fmt.Errorf("no 0x55AA signature at offset %d", offset)
	}
	if bps := binary.LittleEndian.Uint16(boot[11:13]); bps != SectorSize {
		return nil, fmt.Errorf("unsupported sector size %d", bps)
	}
	f := &FAT32Reader{
		r:          r,
		offset:     offset,
		secPerClus: uint32(boot[13]),
		rsvdSecCnt: uint32(binary.LittleEndian.Uint16(boot[14:16])),
		numFATs:    uint32(boot[16]),
		fatSize:    binary.LittleEndian.Uint32(boot[36:40]),
		rootClus:   binary.LittleEndian.Uint32(boot[44:48]),
		label:      strings.TrimRight(string(boot[71:82]), " \x00"),
	}
	if f.secPerClus == 0 || f.fatSize == 0 || f.rootClus < rootClusterNum {
		// FAT16 and FAT12 leave these zero; say so plainly rather than
		// producing nonsense offsets further down.
		//
		// FAT16 과 FAT12 는 이 값들을 0 으로 둔다. 뒤에서 엉뚱한 오프셋을
		// 만들어 내느니 여기서 분명히 말하고 끝낸다.
		return nil, fmt.Errorf("not a FAT32 filesystem at offset %d", offset)
	}
	return f, nil
}

func (f *FAT32Reader) clusterBytes() int { return int(f.secPerClus) * SectorSize }

func (f *FAT32Reader) clusterOffset(c uint32) int64 {
	sector := f.rsvdSecCnt + f.numFATs*f.fatSize + (c-rootClusterNum)*f.secPerClus
	return f.offset + int64(sector)*SectorSize
}

// fatEntry reads one FAT entry, masking off the reserved top 4 bits.
// fatEntry - FAT 엔트리 하나를 읽는다. 예약된 상위 4 비트는 떼어낸다.
func (f *FAT32Reader) fatEntry(c uint32) (uint32, error) {
	buf := make([]byte, 4)
	off := f.offset + int64(f.rsvdSecCnt)*SectorSize + int64(c)*4
	if _, err := f.r.ReadAt(buf, off); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(buf) & 0x0fffffff, nil
}

// chain walks a cluster chain. A corrupt filesystem can contain a loop, so the
// walk is bounded rather than trusted.
//
// chain - 클러스터 체인을 따라간다. 깨진 파일시스템은 고리를 품고 있을 수
// 있으므로, 믿고 도는 대신 상한을 두고 돈다.
func (f *FAT32Reader) chain(first uint32) ([]uint32, error) {
	var out []uint32
	seen := make(map[uint32]bool)
	for c := first; c >= rootClusterNum && c < 0x0ffffff8; {
		if seen[c] {
			return nil, fmt.Errorf("cluster chain loops at %d", c)
		}
		seen[c] = true
		out = append(out, c)
		if len(out) > 1<<20 {
			return nil, fmt.Errorf("cluster chain is implausibly long")
		}
		next, err := f.fatEntry(c)
		if err != nil {
			return nil, err
		}
		c = next
	}
	return out, nil
}

// readChain reads a whole cluster chain into one buffer, cluster aligned - the
// caller trims it to the recorded size.
//
// readChain - 클러스터 체인 전체를 버퍼 하나로 읽는다. 클러스터 단위로
// 정렬돼 있으니 기록된 크기로 자르는 건 호출자 몫이다.
func (f *FAT32Reader) readChain(first uint32) ([]byte, error) {
	chain, err := f.chain(first)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(chain)*f.clusterBytes())
	buf := make([]byte, f.clusterBytes())
	for _, c := range chain {
		if _, err := f.r.ReadAt(buf, f.clusterOffset(c)); err != nil {
			return nil, err
		}
		out = append(out, buf...)
	}
	return out, nil
}

// Entry is one directory entry.
// Entry - 디렉터리 엔트리 하나.
type Entry struct {
	Name    string
	IsDir   bool
	Size    uint32
	Cluster uint32
}

// lfnSlots are the byte offsets of the 13 UCS-2 characters inside a long-name
// record, skipping the attribute, type, checksum and cluster fields.
//
// lfnSlots - long-name 레코드 안에서 UCS-2 문자 13 개가 놓인 바이트 위치.
// 속성·타입·체크섬·클러스터 필드는 건너뛴 자리다.
var lfnSlots = []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}

// ReadDir lists a directory by its first cluster.
// ReadDir - 첫 클러스터로 디렉터리 목록을 읽는다.
func (f *FAT32Reader) ReadDir(cluster uint32) ([]Entry, error) {
	raw, err := f.readChain(cluster)
	if err != nil {
		return nil, err
	}

	var out []Entry
	var lfn []string
	for off := 0; off+dirEntrySize <= len(raw); off += dirEntrySize {
		e := raw[off : off+dirEntrySize]
		switch {
		case e[0] == 0x00:
			return out, nil // end of directory / 디렉터리 끝
		case e[0] == 0xe5:
			lfn = nil
			continue
		}

		if e[11] == attrLFN {
			var units []uint16
			for _, p := range lfnSlots {
				v := binary.LittleEndian.Uint16(e[p : p+2])
				if v == 0x0000 || v == 0xffff {
					break
				}
				units = append(units, v)
			}
			// Records are stored last chunk first, so each one goes in front.
			// 레코드는 마지막 조각부터 저장되므로 읽은 것을 앞에 붙인다.
			lfn = append([]string{string(utf16.Decode(units))}, lfn...)
			continue
		}
		if e[11]&attrVolumeID != 0 {
			lfn = nil
			continue // volume label, not a file / 파일이 아니라 볼륨 레이블
		}

		name := strings.Join(lfn, "")
		lfn = nil
		if name == "" {
			base := strings.TrimRight(string(e[0:8]), " ")
			ext := strings.TrimRight(string(e[8:11]), " ")
			name = base
			if ext != "" {
				name += "." + ext
			}
		}
		if name == "." || name == ".." {
			continue
		}

		out = append(out, Entry{
			Name:  name,
			IsDir: e[11]&attrDirectory != 0,
			Size:  binary.LittleEndian.Uint32(e[28:32]),
			Cluster: uint32(binary.LittleEndian.Uint16(e[20:22]))<<16 |
				uint32(binary.LittleEndian.Uint16(e[26:28])),
		})
	}
	return out, nil
}

// Root lists the root directory.
// Root - 루트 디렉터리 목록.
func (f *FAT32Reader) Root() ([]Entry, error) { return f.ReadDir(f.rootClus) }

// ReadFile returns a file's contents, truncated to its recorded size.
// ReadFile - 파일 내용. 기록된 크기로 잘라서 돌려준다.
func (f *FAT32Reader) ReadFile(e Entry) ([]byte, error) {
	if e.Size == 0 || e.Cluster == 0 {
		return nil, nil
	}
	data, err := f.readChain(e.Cluster)
	if err != nil {
		return nil, err
	}
	if int(e.Size) > len(data) {
		return nil, fmt.Errorf("%s claims %d bytes but only %d are allocated", e.Name, e.Size, len(data))
	}
	return data[:e.Size], nil
}

// WalkFunc is called for every regular file found by Walk. Returning an error
// stops the walk.
//
// WalkFunc - Walk 가 찾은 일반 파일마다 불린다. 에러를 돌려주면 순회가 멈춘다.
type WalkFunc func(p string, e Entry) error

// Walk visits every regular file under dir, depth first.
// Walk - dir 아래의 일반 파일을 깊이 우선으로 전부 방문한다.
func (f *FAT32Reader) Walk(dirCluster uint32, prefix string, fn WalkFunc) error {
	entries, err := f.ReadDir(dirCluster)
	if err != nil {
		return fmt.Errorf("read %s: %w", orRoot(prefix), err)
	}
	for _, e := range entries {
		p := path.Join(prefix, e.Name)
		if e.IsDir {
			if e.Cluster == 0 {
				continue
			}
			if err := f.Walk(e.Cluster, p, fn); err != nil {
				return err
			}
			continue
		}
		if err := fn(p, e); err != nil {
			return err
		}
	}
	return nil
}

// WalkAll walks from the root.
// WalkAll - 루트부터 순회한다.
func (f *FAT32Reader) WalkAll(fn WalkFunc) error { return f.Walk(f.rootClus, "", fn) }

// orRoot names the root directory in an error message, where the empty prefix
// would otherwise read as nothing at all.
//
// orRoot - 에러 메시지에서 루트 디렉터리를 가리킬 이름. 빈 접두사를 그대로
// 쓰면 아무것도 안 적힌 것처럼 보인다.
func orRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}
