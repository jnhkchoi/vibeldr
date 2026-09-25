package image

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"
	"unicode/utf16"
)

// FAT32 is chosen for every partition in a loader image.
//
// GRUB and the DSM loader kernel both read FAT, so using one filesystem
// everywhere lets the whole image be written in user space - no losetup, no
// mkfs, no root. FAT32 rather than FAT16 because a single code path is
// easier to get right than two.
//
// FAT32 - 로더 이미지의 모든 파티션이 쓰는 파일시스템.
//
// GRUB 도 DSM 로더 커널도 FAT 을 읽으므로, 전부 하나로 통일하면 이미지
// 전체를 사용자 공간에서 쓸 수 있다 - losetup 도, mkfs 도, root 도 필요 없다.
// FAT16 이 아니라 FAT32 인 이유는 코드 경로가 둘인 것보다 하나인 편이
// 제대로 만들기 쉬워서다.
type FAT32 struct {
	dev    io.WriterAt
	offset int64 // partition start, in bytes / 파티션 시작, 바이트 단위

	totalSectors uint32
	secPerClus   uint8
	rsvdSecCnt   uint16
	numFATs      uint8
	fatSize      uint32 // sectors per FAT / FAT 하나의 섹터 수
	clusterCount uint32

	volumeID uint32
	label    string

	fat      []uint32 // index = cluster number / 인덱스 = 클러스터 번호
	nextFree uint32

	root   *node
	closed bool
}

const (
	fatFree      uint32 = 0x00000000
	fatEOC       uint32 = 0x0fffffff
	fatMediaByte uint32 = 0x0ffffff8

	rootClusterNum = 2
	dirEntrySize   = 32
	lfnCharsPerRec = 13

	attrReadOnly  = 0x01
	attrHidden    = 0x02
	attrSystem    = 0x04
	attrVolumeID  = 0x08
	attrDirectory = 0x10
	attrArchive   = 0x20
	attrLFN       = attrReadOnly | attrHidden | attrSystem | attrVolumeID

	// FAT32 is only valid above this cluster count; below it the on-disk layout
	// is supposed to be FAT16 and some drivers refuse to mount.
	//
	// 이 클러스터 수를 넘어야 FAT32 로 유효하다. 그 아래는 on-disk 레이아웃이
	// FAT16 이어야 하고, 일부 드라이버는 마운트를 거부한다.
	minFAT32Clusters = 65525
	maxFAT32Clusters = 268435445
)

type node struct {
	name     string
	isDir    bool
	children []*node
	cluster  uint32
	size     uint32
	modTime  time.Time
}

func (n *node) child(name string) *node {
	for _, c := range n.children {
		if strings.EqualFold(c.name, name) {
			return c
		}
	}
	return nil
}

// Geometry is the computed layout of a FAT32 partition.
// Geometry - FAT32 파티션의 계산된 레이아웃.
type Geometry struct {
	SecPerClus   uint8
	RsvdSecCnt   uint16
	NumFATs      uint8
	FATSize      uint32
	ClusterCount uint32
}

// ComputeGeometry works out the cluster size and FAT size for a partition.
//
// The cluster size is the smallest power of two that keeps the FAT itself at a
// sensible size. A one-sector cluster on a multi-gigabyte partition would make
// the FAT tens of megabytes, all of which has to be held in memory and written
// twice.
//
// ComputeGeometry - 파티션에 맞는 cluster 크기와 FAT 크기를 계산한다.
//
// cluster 크기는 FAT 자체가 합리적인 크기로 유지되는 가장 작은 2의 거듭제곱.
// 다중 기가바이트 파티션에서 1 섹터 cluster 를 쓰면 FAT 이 수십 MB 가 되고,
// 그걸 메모리에 다 담고 두 번 써야 한다.
func ComputeGeometry(totalSectors uint32) (Geometry, error) {
	const (
		rsvdSecCnt = 32
		numFATs    = 2
		// Keep each FAT copy at or below 16 MiB.
		// FAT 사본 하나를 16 MiB 이하로 유지한다.
		maxClusters = 4 << 20
	)

	if totalSectors < 1024 {
		return Geometry{}, fmt.Errorf("partition of %d sectors is too small for FAT32", totalSectors)
	}

	for _, secPerClus := range []uint8{1, 2, 4, 8, 16, 32, 64} {
		// Standard FAT32 size calculation (Microsoft FAT specification).
		// 표준 FAT32 크기 계산식 (Microsoft FAT 규격).
		tmp1 := totalSectors - rsvdSecCnt
		tmp2 := (256*uint32(secPerClus) + numFATs) / 2
		fatSize := (tmp1 + tmp2 - 1) / tmp2

		dataSectors := int64(totalSectors) - rsvdSecCnt - int64(numFATs)*int64(fatSize)
		if dataSectors <= 0 {
			continue
		}
		clusters := uint32(dataSectors / int64(secPerClus))

		if clusters < minFAT32Clusters {
			// Too few clusters for a valid FAT32; a bigger cluster only makes
			// it worse, so stop rather than keep growing.
			//
			// FAT32 로 유효하기엔 클러스터가 너무 적다. 클러스터를 키우면
			// 더 나빠지기만 하므로, 계속 키우지 말고 여기서 멈춘다.
			break
		}
		if clusters > maxFAT32Clusters || clusters > maxClusters {
			continue
		}
		return Geometry{
			SecPerClus:   secPerClus,
			RsvdSecCnt:   rsvdSecCnt,
			NumFATs:      numFATs,
			FATSize:      fatSize,
			ClusterCount: clusters,
		}, nil
	}

	return Geometry{}, fmt.Errorf(
		"cannot lay out FAT32 in %d sectors (%.1f MiB): FAT32 needs at least %d clusters",
		totalSectors, float64(totalSectors)*SectorSize/(1<<20), minFAT32Clusters)
}

// Epoch is the timestamp every directory entry in a built image carries.
//
// Real modification times would be noise. The files come from a donor image or
// from a ramdisk repacked moments ago, so a clock value says nothing about what
// the file is. What it would cost is the ability to tell by checksum whether
// the image on a server is the one built here - which only means anything if
// two builds of the same inputs produce the same bytes.
//
// 1980-01-01 is the earliest date the FAT on-disk format can express.
//
// Epoch - 생성된 이미지의 모든 디렉터리 엔트리가 갖는 타임스탬프.
//
// 실제 수정 시각을 쓰면 잡음이다. 파일들은 donor 이미지에서 오거나 조금 전에
// repack 된 ramdisk 에서 오니, 시계값이 파일 실체에 대해 말해 주는 게 없다.
// 대신 잃는 건 서버의 이미지가 여기서 빌드한 이미지인지 체크섬으로 확인하는
// 능력인데, 그건 두 빌드가 같은 입력에서 같은 바이트를 내야 의미를 가진다.
//
// 1980-01-01 은 FAT on-disk 포맷이 표현할 수 있는 가장 이른 날짜.
var Epoch = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)

// FormatFAT32 writes an empty FAT32 filesystem at offset inside dev.
// FormatFAT32 - dev 안의 offset 위치에 빈 FAT32 파일시스템을 쓴다.
func FormatFAT32(dev io.WriterAt, offset int64, totalSectors uint32, label string, volumeID uint32) (*FAT32, error) {
	geo, err := ComputeGeometry(totalSectors)
	if err != nil {
		return nil, err
	}

	f := &FAT32{
		dev:          dev,
		offset:       offset,
		totalSectors: totalSectors,
		secPerClus:   geo.SecPerClus,
		rsvdSecCnt:   geo.RsvdSecCnt,
		numFATs:      geo.NumFATs,
		fatSize:      geo.FATSize,
		clusterCount: geo.ClusterCount,
		volumeID:     volumeID,
		label:        label,
		// +2 because clusters are numbered from 2; entries 0 and 1 are reserved.
		// 클러스터 번호가 2 부터라 +2. 0 번과 1 번 항목은 예약돼 있다.
		fat:      make([]uint32, geo.ClusterCount+2),
		nextFree: rootClusterNum + 1,
		root:     &node{isDir: true, modTime: Epoch},
	}

	f.fat[0] = fatMediaByte
	f.fat[1] = fatEOC
	f.fat[rootClusterNum] = fatEOC // root directory's first cluster / 루트 디렉터리의 첫 클러스터
	f.root.cluster = rootClusterNum

	return f, nil
}

func (f *FAT32) clusterBytes() int { return int(f.secPerClus) * SectorSize }

func (f *FAT32) dataStartSector() uint32 {
	return uint32(f.rsvdSecCnt) + uint32(f.numFATs)*f.fatSize
}

func (f *FAT32) clusterOffset(cluster uint32) int64 {
	sector := f.dataStartSector() + (cluster-rootClusterNum)*uint32(f.secPerClus)
	return f.offset + int64(sector)*SectorSize
}

func (f *FAT32) allocCluster() (uint32, error) {
	for c := f.nextFree; c < f.clusterCount+rootClusterNum; c++ {
		if f.fat[c] == fatFree {
			f.fat[c] = fatEOC
			f.nextFree = c + 1
			return c, nil
		}
	}
	return 0, errors.New("filesystem full: no free clusters left")
}

func (f *FAT32) mkdirAll(p string) (*node, error) {
	cur := f.root
	for _, part := range splitPath(p) {
		next := cur.child(part)
		if next == nil {
			next = &node{name: part, isDir: true, modTime: Epoch}
			cur.children = append(cur.children, next)
		} else if !next.isDir {
			return nil, fmt.Errorf("%s exists and is not a directory", part)
		}
		cur = next
	}
	return cur, nil
}

func splitPath(p string) []string {
	p = strings.ReplaceAll(p, "\\", "/")
	var out []string
	for _, part := range strings.Split(path.Clean("/"+p), "/") {
		if part != "" && part != "." {
			out = append(out, part)
		}
	}
	return out
}

// AddFile streams size bytes from r into the filesystem at path p.
// AddFile - r 에서 size 바이트를 스트리밍해서 파일시스템 안 경로 p 에 쓴다.
func (f *FAT32) AddFile(p string, r io.Reader, size int64) error {
	if f.closed {
		return errors.New("filesystem already closed")
	}
	parts := splitPath(p)
	if len(parts) == 0 {
		return fmt.Errorf("invalid file path %q", p)
	}
	name := parts[len(parts)-1]

	parent := f.root
	if len(parts) > 1 {
		var err error
		parent, err = f.mkdirAll(strings.Join(parts[:len(parts)-1], "/"))
		if err != nil {
			return err
		}
	}
	if parent.child(name) != nil {
		return fmt.Errorf("%s already exists", p)
	}
	if size > int64(^uint32(0)) {
		return fmt.Errorf("%s is %d bytes; FAT32 files cannot exceed 4 GiB", p, size)
	}

	n := &node{name: name, size: uint32(size), modTime: Epoch}

	if size > 0 {
		clusterBytes := f.clusterBytes()
		buf := make([]byte, clusterBytes)
		var first, prev uint32
		remaining := size

		for remaining > 0 {
			chunk := clusterBytes
			if int64(chunk) > remaining {
				chunk = int(remaining)
			}
			if _, err := io.ReadFull(r, buf[:chunk]); err != nil {
				return fmt.Errorf("read data for %s: %w", p, err)
			}
			// The tail of the final cluster must not leak whatever was in the
			// buffer from the previous iteration.
			//
			// 마지막 클러스터의 꼬리로 직전 회차의 버퍼 내용이 새어 나가면
			// 안 된다.
			for i := chunk; i < clusterBytes; i++ {
				buf[i] = 0
			}

			c, err := f.allocCluster()
			if err != nil {
				return fmt.Errorf("%s: %w", p, err)
			}
			if first == 0 {
				first = c
			} else {
				f.fat[prev] = c
			}
			prev = c

			if _, err := f.dev.WriteAt(buf, f.clusterOffset(c)); err != nil {
				return fmt.Errorf("write %s: %w", p, err)
			}
			remaining -= int64(chunk)
		}
		f.fat[prev] = fatEOC
		n.cluster = first
	}

	parent.children = append(parent.children, n)
	return nil
}

// AddFileBytes is AddFile for content that is already in memory.
// AddFileBytes - 이미 메모리에 있는 내용을 위한 AddFile.
func (f *FAT32) AddFileBytes(p string, data []byte) error {
	return f.AddFile(p, strings.NewReader(string(data)), int64(len(data)))
}

// Close allocates the directory clusters and writes the directories, both FAT
// copies and the boot sectors. Nothing on the disk is readable until this
// returns.
//
// Close - 디렉터리 cluster 를 할당하고, 디렉터리와 FAT 사본 두 개, 부트
// 섹터를 쓴다. 이 함수가 리턴할 때까지 디스크에서 읽을 수 있는 건 없다.
func (f *FAT32) Close() error {
	if f.closed {
		return nil
	}
	f.closed = true

	if err := f.allocDirClusters(f.root); err != nil {
		return err
	}
	if err := f.writeDir(f.root, 0); err != nil {
		return err
	}
	if err := f.writeFATs(); err != nil {
		return err
	}
	return f.writeBootSectors()
}

// allocDirClusters reserves all the directory space up front. A directory entry
// has to carry its child's first-cluster number, so the places are claimed
// before anything is serialised.
//
// allocDirClusters - 모든 디렉터리 공간을 먼저 예약한다. 디렉터리 엔트리에는
// 자식의 first-cluster 번호가 들어가야 하므로 직렬화 전에 자리부터 잡는다.
func (f *FAT32) allocDirClusters(dir *node) error {
	entries := f.dirEntryCount(dir)
	perCluster := f.clusterBytes() / dirEntrySize
	needed := (entries + perCluster - 1) / perCluster
	if needed == 0 {
		needed = 1
	}

	// The root already owns cluster 2; extend its chain if it needs more.
	// 루트는 이미 클러스터 2 를 갖고 있다. 더 필요하면 체인을 늘린다.
	prev := dir.cluster
	if prev == 0 {
		c, err := f.allocCluster()
		if err != nil {
			return fmt.Errorf("directory %q: %w", dir.name, err)
		}
		dir.cluster = c
		prev = c
	}
	for i := 1; i < needed; i++ {
		c, err := f.allocCluster()
		if err != nil {
			return fmt.Errorf("directory %q: %w", dir.name, err)
		}
		f.fat[prev] = c
		prev = c
	}
	f.fat[prev] = fatEOC

	for _, child := range dir.children {
		if child.isDir {
			if err := f.allocDirClusters(child); err != nil {
				return err
			}
		}
	}
	return nil
}

// dirEntryCount is how many 32-byte slots a directory takes up, counting the
// long-name records and, for a subdirectory, "." and ".." as well.
//
// dirEntryCount - 디렉터리가 차지하는 32-byte 슬롯 수. long-name 레코드를
// 포함하고, 하위 디렉터리이면 "." 과 ".." 도 포함한다.
func (f *FAT32) dirEntryCount(dir *node) int {
	count := 0
	if dir != f.root {
		count += 2
	} else if f.label != "" {
		count++ // volume label entry / 볼륨 레이블 엔트리
	}
	used := map[string]bool{}
	for _, c := range dir.children {
		short, needLFN := makeShortName(c.name, used)
		used[string(short[:])] = true
		count++
		if needLFN {
			count += lfnRecordCount(c.name)
		}
	}
	return count
}

// lfnRecordCount is how many long-name records a name needs.
// lfnRecordCount - 이름 하나에 필요한 long-name 레코드 수.
func lfnRecordCount(name string) int {
	n := len(utf16.Encode([]rune(name)))
	return (n + lfnCharsPerRec - 1) / lfnCharsPerRec
}

// writeDir serialises one directory into its cluster chain and recurses into
// its subdirectories.
//
// writeDir - 디렉터리 하나를 자기 클러스터 체인으로 직렬화하고 하위
// 디렉터리로 재귀한다.
func (f *FAT32) writeDir(dir *node, parentCluster uint32) error {
	var buf []byte

	// The volume label has to exist as a root directory entry, not only in the
	// BPB. GRUB's `search --label` reads this entry; with the BPB field alone
	// the search silently finds nothing and the boot looks for its kernel on
	// the wrong partition.
	//
	// 볼륨 레이블은 BPB 에만 있으면 안 되고 루트 디렉터리 엔트리로도 있어야
	// 한다. GRUB 의 `search --label` 이 읽는 건 이 엔트리다. BPB 필드만
	// 있으면 검색이 조용히 아무것도 못 찾고, 부팅이 엉뚱한 파티션에서
	// 커널을 찾는다.
	if dir == f.root && f.label != "" {
		var name [11]byte
		copy(name[:], padLabel(f.label))
		buf = append(buf, dirEntry(name, attrVolumeID, 0, 0, dir.modTime)...)
	}

	if dir != f.root {
		buf = append(buf, dirEntry([11]byte{'.', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' '},
			attrDirectory, dir.cluster, 0, dir.modTime)...)
		// ".." points at cluster 0 when the parent is the root directory.
		// 부모가 루트 디렉터리이면 ".." 은 클러스터 0 을 가리킨다.
		up := parentCluster
		if up == rootClusterNum {
			up = 0
		}
		buf = append(buf, dirEntry([11]byte{'.', '.', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' ', ' '},
			attrDirectory, up, 0, dir.modTime)...)
	}

	used := map[string]bool{}
	for _, c := range dir.children {
		short, needLFN := makeShortName(c.name, used)
		used[string(short[:])] = true

		if needLFN {
			buf = append(buf, lfnEntries(c.name, lfnChecksum(short))...)
		}
		attr := byte(attrArchive)
		if c.isDir {
			attr = attrDirectory
		}
		buf = append(buf, dirEntry(short, attr, c.cluster, c.size, c.modTime)...)
	}

	if err := f.writeChain(dir.cluster, buf); err != nil {
		return fmt.Errorf("write directory %q: %w", dir.name, err)
	}

	for _, c := range dir.children {
		if c.isDir {
			if err := f.writeDir(c, dir.cluster); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeChain spreads data across the cluster chain starting at first, zeroing
// whatever room is left over in the last cluster.
//
// writeChain - first 부터 시작하는 cluster 체인에 데이터를 분산 기록한다.
// 마지막 cluster 에 남는 자리는 0 으로 채운다.
func (f *FAT32) writeChain(first uint32, data []byte) error {
	clusterBytes := f.clusterBytes()
	buf := make([]byte, clusterBytes)

	cluster := first
	for off := 0; ; off += clusterBytes {
		for i := range buf {
			buf[i] = 0
		}
		if off < len(data) {
			copy(buf, data[off:])
		}
		if _, err := f.dev.WriteAt(buf, f.clusterOffset(cluster)); err != nil {
			return err
		}
		next := f.fat[cluster]
		if next >= fatEOC || next == fatFree {
			if off+clusterBytes < len(data) {
				return fmt.Errorf("directory needs more clusters than were reserved")
			}
			return nil
		}
		cluster = next
	}
}

// writeFATs serialises the in-memory FAT and writes every copy of it.
// writeFATs - 메모리에 있는 FAT 을 직렬화해 사본 전부를 쓴다.
func (f *FAT32) writeFATs() error {
	raw := make([]byte, int(f.fatSize)*SectorSize)
	for i, v := range f.fat {
		off := i * 4
		if off+4 > len(raw) {
			return fmt.Errorf("FAT overflow: %d entries do not fit in %d sectors", len(f.fat), f.fatSize)
		}
		// The top 4 bits of a FAT32 entry are reserved and must be preserved
		// as zero rather than carrying part of the cluster number.
		//
		// FAT32 엔트리의 상위 4 비트는 예약 영역이다. 클러스터 번호 일부를
		// 담는 게 아니라 0 으로 유지돼야 한다.
		binary.LittleEndian.PutUint32(raw[off:off+4], v&0x0fffffff)
	}

	for i := 0; i < int(f.numFATs); i++ {
		off := f.offset + int64(uint32(f.rsvdSecCnt)+uint32(i)*f.fatSize)*SectorSize
		if _, err := f.dev.WriteAt(raw, off); err != nil {
			return fmt.Errorf("write FAT %d: %w", i, err)
		}
	}
	return nil
}

// writeBootSectors writes the BPB, its backup copy and the two FSInfo sectors.
// writeBootSectors - BPB 와 그 백업 사본, FSInfo 섹터 두 개를 쓴다.
func (f *FAT32) writeBootSectors() error {
	boot := make([]byte, SectorSize)

	// jmp short 0x58; nop - what every real formatter emits.
	// jmp short 0x58; nop - 실제 포매터들이 전부 내보내는 바이트.
	boot[0], boot[1], boot[2] = 0xeb, 0x58, 0x90
	copy(boot[3:11], []byte("MSDOS5.0"))

	binary.LittleEndian.PutUint16(boot[11:13], SectorSize)
	boot[13] = f.secPerClus
	binary.LittleEndian.PutUint16(boot[14:16], f.rsvdSecCnt)
	boot[16] = f.numFATs
	binary.LittleEndian.PutUint16(boot[17:19], 0) // root entries: 0 on FAT32 / 루트 엔트리 수: FAT32 에서는 0
	binary.LittleEndian.PutUint16(boot[19:21], 0) // 16 bit total sectors: unused / 16 비트 총 섹터 수: 안 씀
	boot[21] = 0xf8                               // fixed disk / 고정 디스크
	binary.LittleEndian.PutUint16(boot[22:24], 0) // 16 bit FAT size: unused / 16 비트 FAT 크기: 안 씀
	binary.LittleEndian.PutUint16(boot[24:26], 63)
	binary.LittleEndian.PutUint16(boot[26:28], 255)
	binary.LittleEndian.PutUint32(boot[28:32], uint32(f.offset/SectorSize))
	binary.LittleEndian.PutUint32(boot[32:36], f.totalSectors)

	binary.LittleEndian.PutUint32(boot[36:40], f.fatSize)
	binary.LittleEndian.PutUint16(boot[40:42], 0) // active FAT / mirroring / 활성 FAT 과 미러링
	binary.LittleEndian.PutUint16(boot[42:44], 0) // filesystem version / 파일시스템 버전
	binary.LittleEndian.PutUint32(boot[44:48], rootClusterNum)
	binary.LittleEndian.PutUint16(boot[48:50], 1) // FSInfo sector / FSInfo 섹터
	binary.LittleEndian.PutUint16(boot[50:52], 6) // backup boot sector / 백업 부트 섹터

	boot[64] = 0x80 // BIOS drive number / BIOS 드라이브 번호
	boot[66] = 0x29 // extended boot signature / 확장 부트 서명
	binary.LittleEndian.PutUint32(boot[67:71], f.volumeID)
	copy(boot[71:82], padLabel(f.label))
	copy(boot[82:90], []byte("FAT32   "))

	boot[510], boot[511] = 0x55, 0xaa

	if _, err := f.dev.WriteAt(boot, f.offset); err != nil {
		return fmt.Errorf("write boot sector: %w", err)
	}
	// Backup copy; a filesystem check falls back to it when sector 0 is damaged.
	// 백업 사본. 섹터 0 이 깨지면 파일시스템 검사가 이쪽으로 돌아온다.
	if _, err := f.dev.WriteAt(boot, f.offset+6*SectorSize); err != nil {
		return fmt.Errorf("write backup boot sector: %w", err)
	}

	fsInfo := make([]byte, SectorSize)
	binary.LittleEndian.PutUint32(fsInfo[0:4], 0x41615252)
	binary.LittleEndian.PutUint32(fsInfo[484:488], 0x61417272)
	free := f.clusterCount - (f.nextFree - rootClusterNum)
	binary.LittleEndian.PutUint32(fsInfo[488:492], free)
	binary.LittleEndian.PutUint32(fsInfo[492:496], f.nextFree)
	binary.LittleEndian.PutUint32(fsInfo[508:512], 0xaa550000)

	if _, err := f.dev.WriteAt(fsInfo, f.offset+SectorSize); err != nil {
		return fmt.Errorf("write FSInfo: %w", err)
	}
	if _, err := f.dev.WriteAt(fsInfo, f.offset+7*SectorSize); err != nil {
		return fmt.Errorf("write backup FSInfo: %w", err)
	}
	return nil
}

// padLabel turns a volume label into the fixed 11-byte, upper-case, space
// padded form both the BPB and the root directory entry want.
//
// padLabel - 볼륨 레이블을 BPB 와 루트 디렉터리 엔트리가 요구하는 11 바이트
// 고정폭·대문자·공백 채움 형태로 만든다.
func padLabel(label string) []byte {
	out := []byte("           ") // 11 spaces / 공백 11 개
	up := strings.ToUpper(label)
	for i := 0; i < len(up) && i < 11; i++ {
		out[i] = up[i]
	}
	return out
}

// ---------------------------------------------------------------------------
// directory entries / 디렉터리 엔트리
// ---------------------------------------------------------------------------

// dirEntry builds one 32-byte directory entry.
// dirEntry - 32 바이트 디렉터리 엔트리 하나를 만든다.
func dirEntry(short [11]byte, attr byte, cluster, size uint32, mod time.Time) []byte {
	e := make([]byte, dirEntrySize)
	copy(e[0:11], short[:])
	e[11] = attr
	binary.LittleEndian.PutUint16(e[14:16], fatTime(mod))
	binary.LittleEndian.PutUint16(e[16:18], fatDate(mod))
	binary.LittleEndian.PutUint16(e[18:20], fatDate(mod))
	binary.LittleEndian.PutUint16(e[20:22], uint16(cluster>>16))
	binary.LittleEndian.PutUint16(e[22:24], fatTime(mod))
	binary.LittleEndian.PutUint16(e[24:26], fatDate(mod))
	binary.LittleEndian.PutUint16(e[26:28], uint16(cluster&0xffff))
	binary.LittleEndian.PutUint32(e[28:32], size)
	return e
}

// lfnEntries builds the long-name records that go in front of the short entry.
// They are stored in reverse order, the last chunk first.
//
// lfnEntries - short entry 앞에 오는 long-name 레코드들을 만든다. 저장 순서는
// 역순 (마지막 chunk 가 먼저).
func lfnEntries(name string, checksum byte) []byte {
	chars := utf16.Encode([]rune(name))
	total := lfnRecordCount(name)

	var out []byte
	for seq := total; seq >= 1; seq-- {
		e := make([]byte, dirEntrySize)
		ord := byte(seq)
		if seq == total {
			ord |= 0x40 // last record in the set / 이 묶음의 마지막 레코드
		}
		e[0] = ord
		e[11] = attrLFN
		e[12] = 0
		e[13] = checksum
		// e[26:28] is a first-cluster field that must be zero in an LFN record.
		// e[26:28] 은 first-cluster 필드인데, LFN 레코드에서는 0 이어야 한다.

		// Slots inside one record: 5 chars, then 6, then 2.
		// 레코드 하나 안의 칸 배치: 5 글자, 그다음 6 글자, 그다음 2 글자.
		positions := []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}
		base := (seq - 1) * lfnCharsPerRec
		for i, off := range positions {
			idx := base + i
			var v uint16
			switch {
			case idx < len(chars):
				v = chars[idx]
			case idx == len(chars):
				v = 0x0000 // terminator / 종료 표시
			default:
				v = 0xffff // padding / 채움
			}
			binary.LittleEndian.PutUint16(e[off:off+2], v)
		}
		out = append(out, e...)
	}
	return out
}

// lfnChecksum is the checksum of a short name that ties the long-name records
// to it; a mismatch makes an OS ignore the long name.
//
// lfnChecksum - short 이름의 체크섬. long-name 레코드들을 그 이름에 묶는
// 값이고, 안 맞으면 OS 가 긴 이름을 무시한다.
func lfnChecksum(short [11]byte) byte {
	var sum byte
	for _, c := range short {
		sum = byte(sum&1)<<7 + sum>>1 + c
	}
	return sum
}

// isValidShortChar reports whether a byte may appear in an 8.3 name.
// isValidShortChar - 8.3 이름에 들어갈 수 있는 바이트인지.
func isValidShortChar(c byte) bool {
	switch {
	case c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("$%'-_@~`!(){}^#&", c) >= 0
}

// makeShortName builds an entry's 8.3 name and says whether a long-name record
// is needed as well.
//
// A loader payload is full of names that do not fit 8.3 - bzImage-dsm,
// user-config.yml and the like. grub.cfg fits; initrd-dsm does not. So getting
// this exactly right matters more than convenience.
//
// makeShortName - 엔트리의 8.3 이름을 만들고 long-name 레코드도 필요한지
// 알려 준다.
//
// 로더 페이로드는 8.3 에 안 맞는 이름 투성이다 (bzImage-dsm, user-config.yml
// 같은). grub.cfg 는 되지만 initrd-dsm 은 안 된다. 그래서 편의보다 정확성이
// 중요하다.
func makeShortName(long string, used map[string]bool) ([11]byte, bool) {
	var out [11]byte
	for i := range out {
		out[i] = ' '
	}

	base, ext := long, ""
	if i := strings.LastIndexByte(long, '.'); i > 0 {
		base, ext = long[:i], long[i+1:]
	}

	clean := func(s string, max int) (string, bool) {
		var sb strings.Builder
		lossy := false
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c >= 'a' && c <= 'z' {
				c -= 32
				lossy = true
			}
			if !isValidShortChar(c) {
				c = '_'
				lossy = true
			}
			if sb.Len() < max {
				sb.WriteByte(c)
			} else {
				lossy = true
			}
		}
		return sb.String(), lossy
	}

	shortBase, baseLossy := clean(base, 8)
	shortExt, extLossy := clean(ext, 3)
	needLFN := baseLossy || extLossy || shortBase == ""

	if shortBase == "" {
		shortBase = "_"
	}

	// Disambiguate with the ~N suffix DOS has always used.
	// DOS 의 전통적인 ~N 접미사로 이름 충돌을 푼다.
	candidate := shortBase
	if needLFN || used[key(candidate, shortExt)] {
		for n := 1; n < 1000000; n++ {
			suffix := fmt.Sprintf("~%d", n)
			trimmed := shortBase
			if len(trimmed)+len(suffix) > 8 {
				trimmed = trimmed[:8-len(suffix)]
			}
			candidate = trimmed + suffix
			if !used[key(candidate, shortExt)] {
				break
			}
		}
		needLFN = true
	}

	copy(out[0:8], candidate)
	copy(out[8:11], shortExt)
	return out, needLFN
}

// key is the 11-byte form of a short name, used to spot collisions.
// key - short 이름의 11 바이트 형태. 충돌 판정에 쓴다.
func key(base, ext string) string {
	var out [11]byte
	for i := range out {
		out[i] = ' '
	}
	copy(out[0:8], base)
	copy(out[8:11], ext)
	return string(out[:])
}

// fatDate and fatTime pack a time into the two 16-bit fields the on-disk format
// uses. The date cannot go below 1980 and the time has two-second resolution.
//
// fatDate / fatTime - 시각을 on-disk 포맷의 16 비트 필드 두 개로 채워 넣는다.
// 날짜는 1980 년 아래로 못 가고, 시각의 해상도는 2 초다.
func fatDate(t time.Time) uint16 {
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return uint16((t.Year()-1980)<<9 | int(t.Month())<<5 | t.Day())
}

func fatTime(t time.Time) uint16 {
	return uint16(t.Hour()<<11 | t.Minute()<<5 | t.Second()/2)
}
