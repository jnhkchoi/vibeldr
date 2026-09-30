package catalog

// modules_seed.go - where the driver pack is fetched from.
//
// A Synology kernel carries drivers only for the hardware Synology sells. A
// machine with any other NIC or storage controller finds neither network nor
// disks, and then the DSM install cannot even be started. That is why the
// loader carries a driver pack on partition 4 and loads from it during boot.
//
// The pack is built against the exact DSM kernel by vibeldr-modules and
// published in that repository's releases, two assets per model, gzipped:
// the drivers, <platform>-<model>.cpio.gz (apollolake-DS918+.cpio.gz), and the
// firmware they ask for, <platform>-<model>-firmware.cpio.gz, with a SHA256SUMS
// asset beside them. It holds our builds only; Synology's own
// modules are added from the .pat during the install (kmod.MergeOriginals).
//
// modules_seed.go - 드라이버 팩을 받아 올 곳.
//
// 시놀로지 커널은 시놀로지가 파는 하드웨어의 드라이버만 담고 있다. 그 밖의
// NIC 이나 스토리지 컨트롤러를 꽂은 기계는 네트워크도 디스크도 못 잡고,
// 그러면 DSM 설치 자체를 시작할 수 없다. 로더가 파티션 4 에 드라이버 팩을
// 싣고 부팅 중에 필요한 것만 올리는 이유다.
//
// 팩은 vibeldr-modules 가 DSM 커널에 정확히 맞춰 빌드해 그 저장소의 릴리스에
// 올린다. 모델마다 자산 둘을 gzip 으로 묶는다: 드라이버 `<플랫폼>-<모델>.cpio.gz`
// (예: `apollolake-DS918+.cpio.gz`) 와 그것이 요구하는 펌웨어
// `<플랫폼>-<모델>-firmware.cpio.gz`. 옆에 SHA256SUMS 자산이 있다. 우리 빌드만
// 들어 있고, 시놀로지 자신의 모듈은 설치 중에 .pat 에서 더한다
// (kmod.MergeOriginals).

// ModuleRepo is the GitHub repository whose latest release holds the packs.
// ModuleRepo - 최신 릴리스에 팩을 올려 두는 GitHub 저장소.
const ModuleRepo = "jnhkchoi/vibeldr-modules"

// ModuleSums is the release asset listing the packs' SHA-256.
// ModuleSums - 팩들의 SHA-256 을 적은 릴리스 자산.
const ModuleSums = "SHA256SUMS"

// ModulePackName is the asset name of a model's pack.
// ModulePackName - 모델 팩의 자산 이름.
func ModulePackName(platform, model string) string {
	return platform + "-" + model + ".cpio.gz"
}

// ModuleFirmwareName is the asset name of the firmware for a model's pack: the
// files its drivers ask for, kept apart from the drivers.
//
// ModuleFirmwareName - 모델 팩의 펌웨어 자산 이름. 그 드라이버들이 요구하는
// 파일을 드라이버와 따로 담는다.
func ModuleFirmwareName(platform, model string) string {
	return platform + "-" + model + "-firmware.cpio.gz"
}

// RamdiskBootstrapModules are the modules that go into the DSM ramdisk itself
// rather than into the driver pack.
//
// The pack sits on partition 4 of the loader disk, and seeing that disk at all
// needs the disk controller's driver to be loaded first. A driver that opens
// the disk cannot live on the disk it opens, so the few modules that make the
// disk visible travel inside the ramdisk. The other several hundred are read
// from the pack.
//
// Synology's ramdisk is built for the hardware Synology sells, so USB and SATA
// are already in it and virtio is not - and on a hypervisor the loader disk is
// very often behind virtio.
//
// RamdiskBootstrapModules - 드라이버 팩 전체가 아니라 DSM 램디스크 안에
// 직접 넣어야 하는 모듈들.
//
// 드라이버 팩은 로더 디스크의 파티션 4 에 있다. 그런데 그 디스크를 보려면
// 디스크 컨트롤러 드라이버가 먼저 올라와 있어야 하고, 그 드라이버가 팩
// 안에 들어 있으면 영영 못 연다. 그래서 "디스크를 보이게 만드는 것" 만큼은
// 램디스크 자체에 실어 둔다. 나머지 수백 개는 팩에서 읽는다.
//
// 시놀로지 램디스크는 자기네가 파는 하드웨어 기준이라 USB 와 SATA 는 이미
// 들어 있지만 virtio 는 없다. 하이퍼바이저 위에서는 로더 디스크가 virtio
// 뒤에 있는 경우가 흔하다.
var RamdiskBootstrapModules = []string{
	"virtio", "virtio_ring", "virtio_pci", "virtio_scsi", "virtio_blk",
	"virtio_net",
}
