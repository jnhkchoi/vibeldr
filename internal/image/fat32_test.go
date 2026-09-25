package image

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"strings"
	"testing"
	"unicode/utf16"
)

// memDevice is an in-memory io.WriterAt/io.ReaderAt so the filesystem can be
// built and then read back without touching the disk.
//
// memDevice 는 메모리 위의 io.WriterAt/io.ReaderAt 이다. 디스크를 건드리지
// 않고 파일시스템을 만들고 다시 읽을 수 있다.
type memDevice struct{ data []byte }

func newMemDevice(size int) *memDevice { return &memDevice{data: make([]byte, size)} }

func (m *memDevice) WriteAt(p []byte, off int64) (int, error) {
	if off < 0 || off+int64(len(p)) > int64(len(m.data)) {
		return 0, io.ErrShortWrite
	}
	copy(m.data[off:], p)
	return len(p), nil
}

func (m *memDevice) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(m.data)) {
		return 0, io.EOF
	}
	n := copy(p, m.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// ---------------------------------------------------------------------------
// a minimal FAT32 reader, used only to verify what the writer produced
// 최소한의 FAT32 리더. 기록기가 만든 결과를 검증하는 데만 쓴다.
// ---------------------------------------------------------------------------

type fatReader struct {
	dev    io.ReaderAt
	offset int64

	secPerClus uint32
	rsvdSecCnt uint32
	numFATs    uint32
	fatSize    uint32
	rootClus   uint32
	label      string
	totalSec   uint32
}

func openFAT(t *testing.T, dev io.ReaderAt, offset int64) *fatReader {
	t.Helper()
	boot := make([]byte, SectorSize)
	if _, err := dev.ReadAt(boot, offset); err != nil {
		t.Fatalf("read boot sector: %v", err)
	}
	if boot[510] != 0x55 || boot[511] != 0xaa {
		t.Fatal("boot sector is missing the 0x55AA signature")
	}
	if got := binary.LittleEndian.Uint16(boot[11:13]); got != SectorSize {
		t.Fatalf("bytes per sector = %d, want %d", got, SectorSize)
	}
	if got := string(boot[82:90]); got != "FAT32   " {
		t.Fatalf("filesystem type = %q, want %q", got, "FAT32   ")
	}
	return &fatReader{
		dev:        dev,
		offset:     offset,
		secPerClus: uint32(boot[13]),
		rsvdSecCnt: uint32(binary.LittleEndian.Uint16(boot[14:16])),
		numFATs:    uint32(boot[16]),
		fatSize:    binary.LittleEndian.Uint32(boot[36:40]),
		rootClus:   binary.LittleEndian.Uint32(boot[44:48]),
		label:      strings.TrimRight(string(boot[71:82]), " "),
		totalSec:   binary.LittleEndian.Uint32(boot[32:36]),
	}
}

func (r *fatReader) clusterOffset(c uint32) int64 {
	sector := r.rsvdSecCnt + r.numFATs*r.fatSize + (c-2)*r.secPerClus
	return r.offset + int64(sector)*SectorSize
}

func (r *fatReader) fatEntry(c uint32) uint32 {
	buf := make([]byte, 4)
	off := r.offset + int64(r.rsvdSecCnt)*SectorSize + int64(c)*4
	if _, err := r.dev.ReadAt(buf, off); err != nil {
		return 0
	}
	return binary.LittleEndian.Uint32(buf) & 0x0fffffff
}

// chain walks a cluster chain, guarding against a loop so a writer bug shows up
// as a test failure instead of a hang.
//
// chain 은 클러스터 체인을 따라간다. 고리를 막아 두므로 기록기 버그가 멈춤이
// 아니라 테스트 실패로 드러난다.
func (r *fatReader) chain(first uint32) []uint32 {
	var out []uint32
	seen := map[uint32]bool{}
	for c := first; c >= 2 && c < 0x0ffffff8; c = r.fatEntry(c) {
		if seen[c] {
			return append(out, 0xdeadbeef)
		}
		seen[c] = true
		out = append(out, c)
	}
	return out
}

func (r *fatReader) readClusters(chain []uint32) []byte {
	var out []byte
	buf := make([]byte, int(r.secPerClus)*SectorSize)
	for _, c := range chain {
		if _, err := r.dev.ReadAt(buf, r.clusterOffset(c)); err != nil {
			break
		}
		out = append(out, buf...)
	}
	return out
}

type fatEntryInfo struct {
	Name    string
	IsDir   bool
	Size    uint32
	Cluster uint32
}

// readDir parses a directory, reassembling long file names.
// readDir 는 디렉터리를 파싱하고 긴 파일 이름을 다시 조립한다.
func (r *fatReader) readDir(firstCluster uint32) []fatEntryInfo {
	raw := r.readClusters(r.chain(firstCluster))
	var out []fatEntryInfo
	var lfn []string

	for off := 0; off+dirEntrySize <= len(raw); off += dirEntrySize {
		e := raw[off : off+dirEntrySize]
		switch {
		case e[0] == 0x00:
			return out
		case e[0] == 0xe5:
			lfn = nil
			continue
		}
		if e[11] == attrLFN {
			positions := []int{1, 3, 5, 7, 9, 14, 16, 18, 20, 22, 24, 28, 30}
			var units []uint16
			for _, p := range positions {
				v := binary.LittleEndian.Uint16(e[p : p+2])
				if v == 0x0000 || v == 0xffff {
					break
				}
				units = append(units, v)
			}
			// Records are stored last-first, so prepend.
			// 레코드는 마지막 것부터 저장되므로 앞에 붙인다.
			lfn = append([]string{string(utf16.Decode(units))}, lfn...)
			continue
		}
		if e[11]&attrVolumeID != 0 {
			lfn = nil
			continue
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
		cluster := uint32(binary.LittleEndian.Uint16(e[20:22]))<<16 |
			uint32(binary.LittleEndian.Uint16(e[26:28]))
		out = append(out, fatEntryInfo{
			Name:    name,
			IsDir:   e[11]&attrDirectory != 0,
			Size:    binary.LittleEndian.Uint32(e[28:32]),
			Cluster: cluster,
		})
	}
	return out
}

func (r *fatReader) readFile(e fatEntryInfo) []byte {
	if e.Size == 0 {
		return nil
	}
	data := r.readClusters(r.chain(e.Cluster))
	if int(e.Size) > len(data) {
		return data
	}
	return data[:e.Size]
}

func (r *fatReader) find(entries []fatEntryInfo, name string) (fatEntryInfo, bool) {
	for _, e := range entries {
		if strings.EqualFold(e.Name, name) {
			return e, true
		}
	}
	return fatEntryInfo{}, false
}

// ---------------------------------------------------------------------------
// tests / 테스트
// ---------------------------------------------------------------------------

const testPartMB = 48

func newTestFS(t *testing.T) (*memDevice, *FAT32) {
	t.Helper()
	sectors := uint32(testPartMB * (1 << 20) / SectorSize)
	dev := newMemDevice(int(sectors) * SectorSize)
	fs, err := FormatFAT32(dev, 0, sectors, "TESTVOL", 0x12345678)
	if err != nil {
		t.Fatalf("FormatFAT32: %v", err)
	}
	return dev, fs
}

func TestComputeGeometryProducesValidFAT32(t *testing.T) {
	for _, mb := range []int{48, 64, 128, 512, 1024, 4096, 16384} {
		sectors := uint32(int64(mb) * (1 << 20) / SectorSize)
		geo, err := ComputeGeometry(sectors)
		if err != nil {
			t.Errorf("%d MiB: %v", mb, err)
			continue
		}
		if geo.ClusterCount < minFAT32Clusters {
			t.Errorf("%d MiB: %d clusters is below the FAT32 minimum of %d",
				mb, geo.ClusterCount, minFAT32Clusters)
		}
		// The data area plus metadata must actually fit in the partition.
		// 데이터 영역과 메타데이터가 실제로 파티션 안에 들어가야 한다.
		used := int64(geo.RsvdSecCnt) + int64(geo.NumFATs)*int64(geo.FATSize) +
			int64(geo.ClusterCount)*int64(geo.SecPerClus)
		if used > int64(sectors) {
			t.Errorf("%d MiB: layout needs %d sectors but only %d exist", mb, used, sectors)
		}
		// Every FAT entry must fit in the FAT area.
		// 모든 FAT 엔트리가 FAT 영역 안에 들어가야 한다.
		if int64(geo.ClusterCount+2)*4 > int64(geo.FATSize)*SectorSize {
			t.Errorf("%d MiB: FAT of %d sectors cannot hold %d entries",
				mb, geo.FATSize, geo.ClusterCount+2)
		}
	}
}

func TestComputeGeometryRejectsTinyPartition(t *testing.T) {
	// 16 MiB cannot hold the 65525 clusters FAT32 requires.
	// 16 MiB 에는 FAT32 가 요구하는 65525 클러스터가 안 들어간다.
	if _, err := ComputeGeometry(16 * (1 << 20) / SectorSize); err == nil {
		t.Error("a 16 MiB partition should be rejected for FAT32")
	}
}

func TestFormatWritesReadableBootSector(t *testing.T) {
	dev, fs := newTestFS(t)
	if err := fs.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r := openFAT(t, dev, 0)
	if r.label != "TESTVOL" {
		t.Errorf("label = %q, want TESTVOL", r.label)
	}
	if r.rootClus != rootClusterNum {
		t.Errorf("root cluster = %d, want %d", r.rootClus, rootClusterNum)
	}
	if r.numFATs != 2 {
		t.Errorf("FAT copies = %d, want 2", r.numFATs)
	}

	// The backup boot sector at sector 6 has to match sector 0.
	// 섹터 6 의 백업 부트 섹터는 섹터 0 과 같아야 한다.
	primary := make([]byte, SectorSize)
	backup := make([]byte, SectorSize)
	dev.ReadAt(primary, 0)
	dev.ReadAt(backup, 6*SectorSize)
	if !bytes.Equal(primary, backup) {
		t.Error("backup boot sector does not match the primary")
	}
}

func TestBothFATCopiesMatch(t *testing.T) {
	dev, fs := newTestFS(t)
	if err := fs.AddFileBytes("a.txt", bytes.Repeat([]byte("x"), 10000)); err != nil {
		t.Fatal(err)
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}

	r := openFAT(t, dev, 0)
	size := int(r.fatSize) * SectorSize
	first := make([]byte, size)
	second := make([]byte, size)
	dev.ReadAt(first, int64(r.rsvdSecCnt)*SectorSize)
	dev.ReadAt(second, int64(r.rsvdSecCnt+r.fatSize)*SectorSize)
	if !bytes.Equal(first, second) {
		t.Error("the two FAT copies differ; a driver repairing from the mirror would corrupt the volume")
	}
}

func TestRoundTripShortAndLongNames(t *testing.T) {
	dev, fs := newTestFS(t)

	files := map[string][]byte{
		"grub.cfg":         []byte("set timeout=5\n"),
		"initrd-dsm":       bytes.Repeat([]byte{0xab}, 4096),
		"user-config.yml":  []byte("model: SA6400\n"),
		"VERSION":          []byte("7.4.1-90080"),
		"bzImage-vibeldr":  bytes.Repeat([]byte{0x5a}, 70000),
		"a.very.long.name": []byte("dots"),
	}
	for name, data := range files {
		if err := fs.AddFileBytes(name, data); err != nil {
			t.Fatalf("AddFileBytes(%q): %v", name, err)
		}
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}

	r := openFAT(t, dev, 0)
	entries := r.readDir(r.rootClus)
	if len(entries) != len(files) {
		t.Fatalf("root has %d entries, want %d: %+v", len(entries), len(files), entries)
	}
	for name, want := range files {
		e, ok := r.find(entries, name)
		if !ok {
			t.Errorf("%q is missing from the directory", name)
			continue
		}
		if e.Size != uint32(len(want)) {
			t.Errorf("%q: size = %d, want %d", name, e.Size, len(want))
			continue
		}
		if got := r.readFile(e); !bytes.Equal(got, want) {
			t.Errorf("%q: content round trip failed (%d bytes read)", name, len(got))
		}
	}
}

func TestSubdirectories(t *testing.T) {
	dev, fs := newTestFS(t)

	want := []byte("menuentry 'vibeldr' { linux /bzImage }\n")
	if err := fs.AddFileBytes("boot/grub/grub.cfg", want); err != nil {
		t.Fatal(err)
	}
	if err := fs.AddFileBytes("boot/note.txt", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}

	r := openFAT(t, dev, 0)
	root := r.readDir(r.rootClus)
	boot, ok := r.find(root, "boot")
	if !ok || !boot.IsDir {
		t.Fatalf("boot directory missing from root: %+v", root)
	}

	bootEntries := r.readDir(boot.Cluster)
	// "." and ".." must be the first two entries of a subdirectory.
	// 하위 디렉터리의 첫 두 엔트리는 "." 과 ".." 이어야 한다.
	if len(bootEntries) < 2 || bootEntries[0].Name != "." || bootEntries[1].Name != ".." {
		t.Fatalf("subdirectory is missing its . and .. entries: %+v", bootEntries)
	}

	grub, ok := r.find(bootEntries, "grub")
	if !ok || !grub.IsDir {
		t.Fatalf("grub directory missing: %+v", bootEntries)
	}
	cfg, ok := r.find(r.readDir(grub.Cluster), "grub.cfg")
	if !ok {
		t.Fatal("grub.cfg missing")
	}
	if got := r.readFile(cfg); !bytes.Equal(got, want) {
		t.Errorf("grub.cfg content mismatch: %q", got)
	}
}

func TestLargeFileSpansManyClusters(t *testing.T) {
	dev, fs := newTestFS(t)

	// Deterministic content so a misordered cluster chain is detectable.
	// 내용을 결정적으로 채워 클러스터 체인 순서가 틀리면 드러나게 한다.
	want := make([]byte, 1<<20)
	for i := range want {
		want[i] = byte(i * 7 % 251)
	}
	if err := fs.AddFileBytes("big.bin", want); err != nil {
		t.Fatal(err)
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}

	r := openFAT(t, dev, 0)
	e, ok := r.find(r.readDir(r.rootClus), "big.bin")
	if !ok {
		t.Fatal("big.bin missing")
	}
	chain := r.chain(e.Cluster)
	expected := (len(want) + int(r.secPerClus)*SectorSize - 1) / (int(r.secPerClus) * SectorSize)
	if len(chain) != expected {
		t.Errorf("chain has %d clusters, want %d", len(chain), expected)
	}
	if got := r.readFile(e); !bytes.Equal(got, want) {
		t.Error("large file content does not round trip")
	}
}

// GRUB's `search --label` reads the volume label from a root directory entry,
// not from the BPB. Writing only the BPB field makes the search find nothing,
// and the boot then looks for its kernel on the wrong partition.
//
// GRUB 의 `search --label` 은 볼륨 레이블을 BPB 가 아니라 루트 디렉터리
// 엔트리에서 읽는다. BPB 필드만 쓰면 검색이 아무것도 못 찾고, 부팅이 엉뚱한
// 파티션에서 커널을 찾는다.
func TestVolumeLabelIsARootDirectoryEntry(t *testing.T) {
	dev, fs := newTestFS(t) // label "TESTVOL" / 레이블 "TESTVOL"
	if err := fs.AddFileBytes("a.txt", []byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}

	r := openFAT(t, dev, 0)
	raw := r.readClusters(r.chain(r.rootClus))

	var found string
	for off := 0; off+dirEntrySize <= len(raw); off += dirEntrySize {
		e := raw[off : off+dirEntrySize]
		if e[0] == 0x00 {
			break
		}
		// Exactly the volume-ID bit: the long-name attribute (0x0f) also has
		// it set, so an equality test is required here.
		//
		// 정확히 volume-ID 비트만 봐야 한다. long-name 속성 (0x0f) 도 이 비트가
		// 켜져 있으므로 여기서는 같음 비교가 필요하다.
		if e[11] == attrVolumeID {
			found = strings.TrimRight(string(e[0:11]), " ")
			break
		}
	}
	if found != "TESTVOL" {
		t.Errorf("root directory volume label = %q, want TESTVOL", found)
	}

	// It must not show up as a file.
	// 파일로 보이면 안 된다.
	for _, e := range r.readDir(r.rootClus) {
		if strings.EqualFold(e.Name, "TESTVOL") {
			t.Error("the volume label should not be listed as a file")
		}
	}
}

func TestEmptyFileHasNoCluster(t *testing.T) {
	dev, fs := newTestFS(t)
	if err := fs.AddFileBytes("empty", nil); err != nil {
		t.Fatal(err)
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}

	r := openFAT(t, dev, 0)
	e, ok := r.find(r.readDir(r.rootClus), "empty")
	if !ok {
		t.Fatal("empty file missing")
	}
	if e.Cluster != 0 || e.Size != 0 {
		t.Errorf("empty file should have cluster 0 and size 0, got cluster %d size %d", e.Cluster, e.Size)
	}
}

func TestDuplicateFileRejected(t *testing.T) {
	_, fs := newTestFS(t)
	if err := fs.AddFileBytes("dup", []byte("a")); err != nil {
		t.Fatal(err)
	}
	if err := fs.AddFileBytes("dup", []byte("b")); err == nil {
		t.Error("adding the same path twice should fail rather than silently corrupt the directory")
	}
}

func TestManyFilesSpillOverDirectoryCluster(t *testing.T) {
	dev, fs := newTestFS(t)

	// Each long name costs several 32 byte slots, so a few hundred entries
	// force the root directory onto more than one cluster.
	//
	// 긴 이름 하나가 32 바이트 슬롯을 여러 개 쓰므로, 수백 개 엔트리면 루트
	// 디렉터리가 클러스터 하나를 넘어간다.
	const n = 300
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("a-rather-long-file-name-%03d.dat", i)
		if err := fs.AddFileBytes(name, []byte{byte(i)}); err != nil {
			t.Fatalf("file %d: %v", i, err)
		}
	}
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}

	r := openFAT(t, dev, 0)
	if got := len(r.chain(r.rootClus)); got < 2 {
		t.Fatalf("root directory should span multiple clusters, got %d", got)
	}
	entries := r.readDir(r.rootClus)
	if len(entries) != n {
		t.Fatalf("read %d entries, want %d", len(entries), n)
	}
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("a-rather-long-file-name-%03d.dat", i)
		if _, ok := r.find(entries, name); !ok {
			t.Fatalf("%q missing after spilling to a second cluster", name)
		}
	}
}

func TestShortNamesAreUnique(t *testing.T) {
	used := map[string]bool{}
	seen := map[string]bool{}
	for _, name := range []string{
		"bzImage-vibeldr", "bzImage-dsm", "bzImage-rr",
		"initrd-dsm", "initrd-vibeldr", "user-config.yml",
	} {
		short, needLFN := makeShortName(name, used)
		if !needLFN {
			t.Errorf("%q does not fit 8.3 and should need a long name record", name)
		}
		key := string(short[:])
		if seen[key] {
			t.Errorf("%q collides with an earlier short name %q", name, key)
		}
		seen[key] = true
		used[key] = true
	}
}

func TestShortNameKeepsValid83Unchanged(t *testing.T) {
	used := map[string]bool{}
	short, needLFN := makeShortName("GRUB.CFG", used)
	if needLFN {
		t.Error("an already valid 8.3 name should not need a long name record")
	}
	if got := strings.TrimRight(string(short[0:8]), " "); got != "GRUB" {
		t.Errorf("base = %q, want GRUB", got)
	}
	if got := strings.TrimRight(string(short[8:11]), " "); got != "CFG" {
		t.Errorf("ext = %q, want CFG", got)
	}
}

// The checksum ties a long name to its short entry; if it is wrong, every
// driver silently ignores the long name and shows the 8.3 one instead.
//
// 체크섬은 긴 이름을 short 엔트리에 묶는다. 틀리면 모든 드라이버가 긴 이름을
// 조용히 무시하고 8.3 이름을 보여 준다.
func TestLFNChecksum(t *testing.T) {
	cases := map[string]byte{
		"TEXTFILETXT": 0x79,
		"GRUB    CFG": 0x92,
		"BZIMAG~1   ": 0x15,
	}
	for name, want := range cases {
		var short [11]byte
		copy(short[:], name)
		if got := lfnChecksum(short); got != want {
			t.Errorf("lfnChecksum(%q) = 0x%02x, want 0x%02x", name, got, want)
		}
	}
}

// endlessReader supplies as many bytes as asked for, so the failure under test
// is the allocator running out of clusters rather than the source running dry.
//
// endlessReader 는 요청한 만큼 바이트를 계속 준다. 그래서 여기서 검증하는
// 실패는 원본이 바닥나는 게 아니라 할당기의 클러스터가 떨어지는 것이다.
type endlessReader struct{}

func (endlessReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestFilesystemFullIsReported(t *testing.T) {
	_, fs := newTestFS(t)
	// Far larger than the 48 MiB partition.
	// 48 MiB 파티션보다 훨씬 크다.
	err := fs.AddFile("huge", endlessReader{}, 64<<20)
	if err == nil {
		t.Fatal("writing past the end of the filesystem should fail")
	}
	if !strings.Contains(err.Error(), "full") {
		t.Errorf("error should say the filesystem is full, got: %v", err)
	}
}
