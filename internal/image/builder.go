package image

import (
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// maxDonorFileBytes is the cap that stops something enormous on a donor
// partition being pulled into memory whole. GRUB modules and themes are far
// smaller than this.
//
// maxDonorFileBytes - donor 파티션에서 뭔가 엄청난 걸 통째로 메모리에
// 끌어오지 않도록 하는 상한. GRUB 모듈과 테마는 이것보다 훨씬 작다.
const maxDonorFileBytes = 32 << 20

// donorGeneratedPaths are the files we always write ourselves; a donor's copy
// must not overwrite them.
//
// donorGeneratedPaths - 우리가 항상 직접 쓰는 파일. donor 의 사본으로
// 덮이면 안 된다.
var donorGeneratedPaths = map[string]bool{
	"boot/grub/grub.cfg": true,
	"boot/grub/grubenv":  true,
}

// FirstPartitionLBA is where partition 1 starts.
//
// It is the modern 1 MiB alignment. The 1 MiB left in front of the first
// partition is also the BIOS boot gap that the GRUB core image is embedded in.
// Starting at sector 63, as older layouts did, leaves only 31 KiB there, and a
// GRUB core carrying the FAT and part_msdos modules does not fit in that.
//
// FirstPartitionLBA - 파티션 1 의 시작.
//
// 요즘 기본인 1 MiB 정렬이다. 첫 파티션 앞에 남는 1 MiB 가 GRUB core image 를
// embed 하는 BIOS boot gap 이기도 하다. 옛날처럼 63 섹터에서 시작하면 여기가
// 31 KiB 밖에 안 남는데, FAT + part_msdos 모듈을 담은 GRUB core 는
// 그 안에 안 들어간다.
const FirstPartitionLBA = 2048

// File is one file to place on a partition.
//
// Exactly one of Source and Data is used. Source streams from disk, so even a
// 100 MB ramdisk never has to be held in memory whole.
//
// File - 파티션에 넣을 파일 항목 하나.
//
// Source 와 Data 중 정확히 하나만 쓴다. Source 는 디스크에서 스트리밍하므로
// 100MB 짜리 램디스크도 메모리에 통째로 안 담아도 된다.
type File struct {
	// Path is the destination path inside the partition, e.g.
	// "boot/grub/grub.cfg".
	//
	// Path - 파티션 안 목적지 경로 (예: "boot/grub/grub.cfg").
	Path string
	// Source is a local file path, the source of the copy.
	// Source - 로컬 파일 경로. 복사 원본.
	Source string
	// Data is the inline bytes used when Source is empty.
	// Data - Source 가 비어있을 때 사용할 인라인 바이트.
	Data []byte
}

// Content is what goes on each partition.
// Content - 각 파티션에 들어갈 내용.
type Content struct {
	P1 []File
	P2 []File
	P3 []File
}

// Builder describes the image to produce.
// Builder - 만들어 낼 이미지의 서술.
type Builder struct {
	// TotalMB is the whole image size. Zero picks a size that fits the
	// requested partitions plus some working room.
	//
	// TotalMB - 이미지 전체 크기. 0 이면 요청한 파티션들과 작업 여유를
	// 합쳐 들어가는 크기를 고른다.
	TotalMB int
	// P1MB and P2MB are the fixed-size partitions. Partition 3 takes whatever
	// is left, minus P4MB when a driver pack is carried.
	//
	// P1MB, P2MB - 고정 크기 파티션. 파티션 3 은 나머지를 다 가진다. 드라이버
	// 팩이 실리면 P4MB 만큼 더 뺀다.
	P1MB int
	P2MB int
	// P4MB is the size of the raw archive partition; 0 when no pack is carried.
	// P4MB - raw 아카이브 파티션 크기. 팩을 안 실으면 0.
	P4MB int
	// P4 is the bytes that go on it, as they are, with no filesystem.
	// P4 - 그 파티션에 들어갈 바이트 (파일시스템 없이 그대로).
	P4 []byte

	// Labels are the FAT volume labels, in partition order.
	// Labels - FAT 볼륨 라벨, 파티션 순서대로.
	Labels [3]string

	// DonorPath is an existing loader image to take the MBR boot code and the
	// BIOS boot gap from. vibeldr embeds its own GRUB, so this is usually not
	// needed; when set, the donor's copy wins. It is a compatibility path.
	//
	// DonorPath - MBR boot code 와 BIOS boot gap 을 회수해올 기존 로더 이미지.
	// vibeldr 이 GRUB 을 자체 embed 하므로 대개 필요 없다. 지정하면 그쪽
	// 사본이 우선한다 (호환성 대체 경로).
	DonorPath string

	// VolumeIDSeed makes the FAT volume serial numbers deterministic, so two
	// builds of the same configuration come out byte-identical.
	//
	// VolumeIDSeed - FAT 볼륨 시리얼 번호를 결정론적으로 만드는 시드. 같은
	// 설정의 두 빌드가 byte-identical 이 되게 한다.
	VolumeIDSeed uint32

	// WithEFI puts \EFI\BOOT\BOOTX64.EFI and a short grub.cfg stub on the first
	// partition, so the image also boots on a UEFI machine with CSM off. The
	// default, false, gives a BIOS-only image. The BIOS boot path
	// (MBR + core.img) is always written regardless.
	//
	// No command sets it today, so every image is BIOS-only. It is kept so UEFI
	// boot can be switched on later.
	//
	// WithEFI - true 로 켜면 첫 파티션에 \EFI\BOOT\BOOTX64.EFI 와 짧은
	// grub.cfg 스텁을 얹어 UEFI (CSM off) 머신에서도 부팅되게 한다.
	// 기본값 false 이면 BIOS 전용 이미지가 된다. BIOS 부트 경로
	// (MBR + core.img) 는 이 값과 무관하게 항상 쓴다.
	//
	// 지금은 이 값을 켜는 명령이 없어 모든 이미지가 BIOS 전용이다. 나중에
	// UEFI 부팅을 켤 수 있게 남겨 둔다.
	WithEFI bool
}

// DefaultBuilder is the three-partition geometry: a small boot partition,
// a small partition for the untouched DSM originals, and the rest for the
// payload that gets rebuilt on every run.
//
// DefaultBuilder - 3 파티션 기본 배치. 작은 부트 파티션, 손대지 않은 DSM
// 원본을 담을 작은 파티션, 그리고 매 실행마다 다시 만들어지는 페이로드가
// 들어갈 나머지 전부.
func DefaultBuilder() *Builder {
	return &Builder{
		TotalMB: 1024,
		P1MB:    128,
		P2MB:    128,
		Labels:  [3]string{"VIBELDR1", "VIBELDR2", "VIBELDR3"},
	}
}

// Result summarises what was written.
// Result - 무엇이 써졌는지의 요약.
type Result struct {
	SizeBytes  int64
	Partitions []Partition
	// DonatedFiles is how many files were copied from the donor's boot
	// partition.
	//
	// DonatedFiles - donor 부트 파티션에서 복사된 파일 수.
	DonatedFiles int
	// Warnings are situations that do not fail the build but make it very
	// likely the resulting image will not boot.
	//
	// Warnings - 빌드는 실패시키지 않지만 결과 이미지가 부팅 안 될
	// 가능성이 매우 높은 상황들.
	Warnings []string
}

// Build creates the image file.
// Build - 이미지 파일 생성.
func (b *Builder) Build(outPath string, content *Content) (*Result, error) {
	if content == nil {
		content = &Content{}
	}
	if err := b.validate(); err != nil {
		return nil, err
	}

	totalSectors := uint32(int64(b.TotalMB) * (1 << 20) / SectorSize)
	p1Sectors := uint32(int64(b.P1MB) * (1 << 20) / SectorSize)
	p2Sectors := uint32(int64(b.P2MB) * (1 << 20) / SectorSize)

	p1 := Partition{Bootable: true, Type: TypeFAT32LBA, StartLBA: FirstPartitionLBA, Sectors: p1Sectors}
	p2 := Partition{Type: TypeFAT32LBA, StartLBA: p1.End(), Sectors: p2Sectors}
	if p2.End() >= totalSectors {
		return nil, fmt.Errorf("partitions 1 and 2 (%d + %d MiB) do not fit in a %d MiB image",
			b.P1MB, b.P2MB, b.TotalMB)
	}
	// Partition 4 holds the driver pack, as a plain archive with no filesystem
	// on it at all.
	//
	// The obvious arrangement - another FAT partition, mounted at boot - does
	// not work here. Synology's kernel refuses every vfat mount in the ramdisk
	// stage with EINVAL and logs nothing, while the same image mounts fine on
	// an ordinary Linux machine, so the refusal is theirs rather than a fault
	// in the filesystem. So the loader does not need a filesystem there: the
	// partition is a cpio archive written end to end, and the helper reads it
	// from the block device and unpacks it in memory with the same cpio reader
	// the ramdisk uses.
	//
	// 파티션 4 는 드라이버 팩을 담되, 파일시스템이 전혀 없는 평범한
	// 아카이브로 담는다.
	//
	// 뻔한 방식 - FAT 파티션을 하나 더 만들어 부팅 때 마운트 - 은 여기서
	// 통하지 않는다. 시놀로지 커널은 램디스크 단계의 모든 vfat 마운트를
	// EINVAL 로 거부하고 로그도 안 남기는데, 같은 이미지가 평범한 리눅스
	// 머신에서는 멀쩡히 마운트된다. 파일시스템 문제가 아니라 그쪽의 거부다.
	// 그래서 로더는 거기서 파일시스템을 쓰지 않는다. 파티션은 처음부터
	// 끝까지 cpio 아카이브이고, 헬퍼가 블록 장치에서 읽어 램디스크와 같은
	// cpio 리더로 메모리에서 푼다.
	p4Sectors := uint32(int64(b.P4MB) * (1 << 20) / SectorSize)
	rest := totalSectors - p2.End()
	if p4Sectors >= rest {
		return nil, fmt.Errorf("partition 4 (%d MiB) leaves nothing for partition 3 in a %d MiB image",
			b.P4MB, b.TotalMB)
	}
	p3 := Partition{Type: TypeFAT32LBA, StartLBA: p2.End(), Sectors: rest - p4Sectors}
	parts := []Partition{p1, p2, p3}
	if p4Sectors > 0 {
		parts = append(parts, Partition{Type: TypeLinux, StartLBA: p3.End(), Sectors: p4Sectors})
	}

	// Fail before creating anything if a partition is too small for FAT32.
	// The fourth carries no filesystem, so it is not held to that.
	//
	// 파티션이 FAT32 에 비해 너무 작으면 아무것도 만들기 전에 실패시킨다.
	// 네 번째는 파일시스템이 없으므로 이 조건이 걸리지 않는다.
	for i, p := range parts[:3] {
		if _, err := ComputeGeometry(p.Sectors); err != nil {
			return nil, fmt.Errorf("partition %d: %w", i+1, err)
		}
	}

	if dir := filepath.Dir(outPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create %s: %w", dir, err)
		}
	}

	f, err := os.Create(outPath)
	if err != nil {
		return nil, fmt.Errorf("create %s: %w", outPath, err)
	}
	// A sparse file of the right length: the holes cost nothing on disk until
	// something is written into them.
	//
	// 길이만 맞춘 sparse 파일이다. 구멍은 뭔가 써 넣기 전까지 디스크를
	// 차지하지 않는다.
	size := int64(totalSectors) * SectorSize
	if err := f.Truncate(size); err != nil {
		f.Close()
		os.Remove(outPath)
		return nil, fmt.Errorf("size %s: %w", outPath, err)
	}

	// Everything below writes into f; on any failure the partial image is
	// removed rather than left behind looking usable.
	//
	// 아래는 전부 f 에 쓴다. 실패하면 만들다 만 이미지를 쓸 수 있어 보이는
	// 채로 남기지 않고 지운다.
	fail := func(err error) (*Result, error) {
		f.Close()
		os.Remove(outPath)
		return nil, err
	}

	var bootCode []byte
	var warnings []string
	donatedCount := 0
	if b.DonorPath == "" {
		// GRUB of our own. Two files, no second image to lift it from, and
		// nothing on the boot partition but the menu - every module GRUB needs
		// is compiled into core.img.
		//
		// 우리 GRUB 이다. 파일 두 개뿐이고, 뜯어올 두 번째 이미지도 없고,
		// 부트 파티션에는 메뉴 말고 아무것도 없다. GRUB 이 필요한 모듈은
		// 전부 core.img 안에 컴파일돼 있다.
		bootCode, err = writeGRUB(f)
		if err != nil {
			return fail(err)
		}
	} else {
		bootCode, err = b.copyDonorBootArea(f)
		if err != nil {
			return fail(err)
		}
		// The donor's GRUB core is only a stub: it loads normal.mod and the
		// rest of GRUB from the boot partition at run time. Without those the
		// boot stops at `grub rescue>`, so they have to come along.
		//
		// donor 의 GRUB core 는 스텁일 뿐이다. 실행 중에 normal.mod 와 나머지
		// GRUB 을 부트 파티션에서 로드한다. 그게 없으면 부팅이
		// `grub rescue>` 에서 멈추므로 함께 가져와야 한다.
		donated, derr := donorP1Files(b.DonorPath, content.P1)
		if derr != nil {
			// Not fatal - a donor might keep its boot files on a filesystem we
			// cannot read - but the image almost certainly will not boot, so
			// say so instead of producing a quiet dud.
			//
			// 치명적이지는 않다. donor 가 우리가 못 읽는 파일시스템에 부트
			// 파일을 둘 수도 있다. 다만 그 이미지는 거의 확실히 부팅이 안
			// 되므로, 조용한 불량품을 내놓는 대신 그렇게 말해 준다.
			warnings = append(warnings, fmt.Sprintf(
				"could not read GRUB modules from the donor's first partition (%v); "+
					"the image will likely stop at `grub rescue>`", derr))
		} else {
			content.P1 = append(donated, content.P1...)
			donatedCount = len(donated)
		}
	}

	seed := b.VolumeIDSeed
	if seed == 0 {
		seed = contentSeed(b, content)
	}
	if err := WriteMBR(f, parts, bootCode, seed); err != nil {
		return fail(err)
	}

	if len(b.P4) > 0 {
		room := int64(p4Sectors) * SectorSize
		if int64(len(b.P4)) > room {
			return fail(fmt.Errorf("the driver pack is %d bytes and partition 4 holds %d", len(b.P4), room))
		}
		if _, err := f.WriteAt(b.P4, int64(p3.End())*SectorSize); err != nil {
			return fail(fmt.Errorf("write partition 4: %w", err))
		}
	}

	// Only the first three carry a filesystem; the fourth is a raw archive and
	// has already been written.
	//
	// 앞의 세 개만 파일시스템을 담는다. 네 번째는 raw 아카이브이고 이미
	// 써 두었다.
	perPartition := [][]File{content.P1, content.P2, content.P3}
	for i, p := range parts[:len(perPartition)] {
		fs, err := FormatFAT32(f, int64(p.StartLBA)*SectorSize, p.Sectors, b.Labels[i], seed+uint32(i)+1)
		if err != nil {
			return fail(fmt.Errorf("format partition %d: %w", i+1, err))
		}
		if err := writeFiles(fs, perPartition[i]); err != nil {
			return fail(fmt.Errorf("partition %d: %w", i+1, err))
		}
		// The UEFI boot files go only on the first partition (P1). Firmware
		// never opens the other partitions directly, on BIOS or UEFI.
		//
		// UEFI 부팅용 파일은 첫 파티션 (P1) 에만 얹는다. 나머지 파티션은
		// BIOS/UEFI 어느 쪽에서도 펌웨어가 직접 열지 않는다.
		if i == 0 && b.WithEFI {
			if err := writeEFIToFAT(fs); err != nil {
				return fail(fmt.Errorf("partition %d efi payload: %w", i+1, err))
			}
		}
		// The placeholder the DSM installer looks for. Always written,
		// regardless of UEFI support - without it the last step of the install
		// (updating the factory partition) fails.
		//
		// DSM 인스톨러가 찾는 자리표시자. UEFI 지원과 무관하게 항상 쓴다.
		// 없으면 설치 마지막 단계(factory partition 갱신)가 실패한다.
		if i == 0 {
			if err := writeSynoBootLoaderStub(fs); err != nil {
				return fail(fmt.Errorf("partition %d SynoBootLoader stub: %w", i+1, err))
			}
		}
		if err := fs.Close(); err != nil {
			return fail(fmt.Errorf("finalise partition %d: %w", i+1, err))
		}
	}

	if err := f.Sync(); err != nil {
		return fail(fmt.Errorf("sync %s: %w", outPath, err))
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close %s: %w", outPath, err)
	}

	return &Result{
		SizeBytes:    size,
		Partitions:   parts,
		DonatedFiles: donatedCount,
		Warnings:     warnings,
	}, nil
}

// MinPartitionMB is the smallest partition FAT32 can be laid out in: below this
// there are fewer than the 65525 clusters the format requires.
//
// MinPartitionMB - FAT32 를 앉힐 수 있는 최소 파티션 크기. 이보다 작으면
// 포맷이 요구하는 65525 클러스터가 안 나온다.
const MinPartitionMB = 48

// MinImageMB is three minimum partitions plus 16 MiB, which covers the 1 MiB
// alignment gap in front of partition 1 with room to spare.
//
// MinImageMB - 최소 파티션 셋에 16 MiB 를 더한 값. 파티션 1 앞의 1 MiB 정렬
// 간격을 넉넉히 덮는다.
const MinImageMB = MinPartitionMB*3 + 16

func (b *Builder) validate() error {
	var problems []string
	if b.TotalMB < MinImageMB {
		problems = append(problems, fmt.Sprintf(
			"total size %d MiB is too small; %d MiB is the minimum for three FAT32 partitions",
			b.TotalMB, MinImageMB))
	}
	if b.P1MB < MinPartitionMB {
		problems = append(problems, fmt.Sprintf("partition 1 is %d MiB; FAT32 needs at least %d MiB", b.P1MB, MinPartitionMB))
	}
	if b.P2MB < MinPartitionMB {
		problems = append(problems, fmt.Sprintf("partition 2 is %d MiB; FAT32 needs at least %d MiB", b.P2MB, MinPartitionMB))
	}
	if len(problems) > 0 {
		msg := "invalid image layout:"
		for _, p := range problems {
			msg += "\n  - " + p
		}
		return errors.New(msg)
	}
	return nil
}

// copyDonorBootArea copies everything in front of the first partition from an
// existing loader image: the MBR and the BIOS boot gap that holds GRUB's core
// image. It returns the donor's MBR boot code so the caller can keep it while
// writing our own partition table.
//
// copyDonorBootArea - 기존 로더 이미지에서 첫 파티션 앞의 모든 것을 베낀다.
// MBR 과, GRUB core 이미지가 들어앉는 BIOS 부트 간격이다. 도너의 MBR 부트
// 코드를 돌려주므로, 호출자는 자기 파티션 테이블을 쓰면서 그건 살려 둘 수 있다.
func (b *Builder) copyDonorBootArea(dst io.WriterAt) ([]byte, error) {
	donor, err := os.Open(b.DonorPath)
	if err != nil {
		return nil, fmt.Errorf("open donor %s: %w", b.DonorPath, err)
	}
	defer donor.Close()

	donorParts, err := ReadMBR(donor)
	if err != nil {
		return nil, fmt.Errorf("donor %s: %w", b.DonorPath, err)
	}
	// If the donor's first partition starts later than ours, its boot gap is
	// larger than ours and its embedded GRUB core would be truncated by our
	// partition 1. Refuse rather than produce an image that fails in the
	// middle of GRUB's second stage.
	//
	// 도너의 첫 파티션이 우리 것보다 뒤에서 시작하면 도너의 부트 간격이 우리
	// 것보다 커서, 거기 박힌 GRUB core 를 우리 파티션 1 이 잘라먹는다. GRUB 2 단계 도중에 죽는 이미지를 만드느니
	// 여기서 거부한다.
	if donorParts[0].StartLBA > FirstPartitionLBA {
		return nil, fmt.Errorf(
			"donor %s starts its first partition at LBA %d, past our %d; its boot gap would not fit",
			b.DonorPath, donorParts[0].StartLBA, FirstPartitionLBA)
	}

	gap := make([]byte, FirstPartitionLBA*SectorSize)
	if _, err := donor.ReadAt(gap, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read donor boot area: %w", err)
	}
	if _, err := dst.WriteAt(gap, 0); err != nil {
		return nil, fmt.Errorf("write boot area: %w", err)
	}

	bootCode := make([]byte, mbrBootCodeSize)
	copy(bootCode, gap)
	return bootCode, nil
}

// donorP1Files reads every regular file from the donor's first partition,
// skipping anything the caller is writing itself.
//
// donorP1Files - 도너의 첫 파티션에서 일반 파일을 전부 읽는다. 호출자가
// 직접 쓰는 것은 건너뛴다.
func donorP1Files(donorPath string, own []File) ([]File, error) {
	skip := make(map[string]bool, len(own)+len(donorGeneratedPaths))
	for k := range donorGeneratedPaths {
		skip[k] = true
	}
	for _, f := range own {
		skip[strings.ToLower(strings.TrimPrefix(f.Path, "/"))] = true
	}

	donor, err := os.Open(donorPath)
	if err != nil {
		return nil, err
	}
	defer donor.Close()

	parts, err := ReadMBR(donor)
	if err != nil {
		return nil, err
	}
	fr, err := OpenFAT32(donor, int64(parts[0].StartLBA)*SectorSize)
	if err != nil {
		return nil, fmt.Errorf("partition 1: %w", err)
	}

	var out []File
	err = fr.WalkAll(func(p string, e Entry) error {
		if skip[strings.ToLower(p)] {
			return nil
		}
		if e.Size > maxDonorFileBytes {
			return nil
		}
		data, err := fr.ReadFile(e)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		out = append(out, File{Path: p, Data: data})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// writeFiles writes a list of Files into the filesystem, streaming the ones
// backed by a path and copying the ones already in memory.
//
// writeFiles - File 목록을 파일시스템에 쓴다. 경로가 있는 것은 흘려보내고
// 메모리에 이미 있는 것은 그대로 복사한다.
func writeFiles(fs *FAT32, files []File) error {
	for _, file := range files {
		if file.Source != "" {
			src, err := os.Open(file.Source)
			if err != nil {
				return fmt.Errorf("open %s: %w", file.Source, err)
			}
			st, err := src.Stat()
			if err != nil {
				src.Close()
				return fmt.Errorf("stat %s: %w", file.Source, err)
			}
			err = fs.AddFile(file.Path, src, st.Size())
			src.Close()
			if err != nil {
				return err
			}
			continue
		}
		if err := fs.AddFileBytes(file.Path, file.Data); err != nil {
			return err
		}
	}
	return nil
}

// contentSeed derives the FAT volume serial numbers from what the image holds.
//
// The serial only has to tell two volumes apart, so the clock would be an easy
// source for it - and an easy way to make every build of the same inputs come
// out different, which is exactly what makes a checksum useless. The hash
// covers the layout, the labels, and each file's path and size (not its
// bytes), so identical inputs give identical serials and a change in any of
// those gives a different one.
//
// contentSeed - 이미지가 담고 있는 내용에서 FAT 볼륨 일련번호를 뽑는다.
//
// 일련번호는 볼륨 둘을 구분만 하면 되니 시계에서 가져오기 쉽다 - 그리고 같은
// 입력으로 빌드할 때마다 결과가 달라지게 만들기도 쉽다. 그러면 체크섬이
// 무의미해진다. 해시는 배치, 라벨, 각 파일의 경로와 크기를 덮는다 (바이트
// 내용은 아니다). 그래서 같은 입력은 같은 일련번호를, 그중 하나라도 바뀌면
// 다른 일련번호를 낸다.
func contentSeed(b *Builder, content *Content) uint32 {
	h := fnv.New32a()
	fmt.Fprintf(h, "%d/%d/%d/%s", b.TotalMB, b.P1MB, b.P2MB, strings.Join(b.Labels[:], ","))
	for _, p := range [][]File{content.P1, content.P2, content.P3} {
		for _, f := range p {
			fmt.Fprintf(h, "|%s:%d", f.Path, len(f.Data))
			if f.Source != "" {
				if st, err := os.Stat(f.Source); err == nil {
					fmt.Fprintf(h, "@%d", st.Size())
				}
			}
		}
	}
	return h.Sum32()
}
