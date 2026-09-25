package cmdline

import (
	"fmt"
	"sort"
	"strings"

	"vibeldr/internal/catalog"
	"vibeldr/internal/config"
	"vibeldr/internal/hwscan"
)

// Options are the facts only the target machine knows. They are passed in from
// outside so that Build stays a pure function of config plus Options.
//
// Options - 타겟 머신만 아는 사실들. Build 자체가 config + Options 의
// 순수 함수로 유지되도록 이 값들을 밖에서 받는다.
type Options struct {
	// EFI reports whether this machine boots UEFI rather than legacy BIOS.
	// EFI - 이 머신이 legacy BIOS 대신 UEFI 로 부팅하는지.
	EFI bool
	// LoaderBus is how the loader disk is attached: usb, sata, nvme, mmc...
	// LoaderBus - 로더 디스크가 붙어있는 방식: usb, sata, nvme, mmc, ...
	LoaderBus string
	// LoaderSizeMB is the loader disk's size, needed for dom_szmax on kernel
	// 4.x. 0 leaves the parameter out.
	//
	// LoaderSizeMB - 로더 디스크 크기. kernel 4.x 의 dom_szmax 에 필요하다.
	// 0 이면 파라미터를 안 붙인다.
	LoaderSizeMB int
	// NICProfile is the result of hwscan.DetectNICProfile, used to turn
	// sortnetif on automatically when vendors are mixed. The zero value (the
	// default) counts as "no vendor detected" and leaves sortnetif off.
	//
	// NICProfile - hwscan.DetectNICProfile 결과. 벤더가 섞인 경우 sortnetif 를
	// 자동으로 켜기 위해 쓴다. 제로 값이면 (기본) 어떤 벤더도 감지 안 된 것으로
	// 간주되어 sortnetif 는 켜지지 않는다.
	NICProfile hwscan.NICProfile

	// Controllers is the disk controller list hwscan sorted into bay order. On
	// a non-DT platform SataPortMap and DiskIdxMap are computed from it. Empty
	// means no computation and a fallback that uses only the bay count.
	//
	// Controllers - hwscan 이 베이 순서로 정렬해 준 디스크 컨트롤러 목록.
	// 비-DT 플랫폼에서 SataPortMap/DiskIdxMap 을 이 구성에서 계산한다.
	// 비어 있으면 계산하지 않고 베이 수만 쓰는 폴백으로 간다.
	Controllers []hwscan.Controller

	// NVMeOnly is true when hwscan found a machine made of NVMe alone, with no
	// SATA or SAS controller. autorender's NVMeSystemWanted ORs this together
	// with the explicit option in cfg (Storage.NVMeSystem) to reach the final
	// answer.
	//
	// The value is a hint rather than something that changes the command line
	// by itself, so leaving it false when unsure is the safe choice. The user
	// can always force it through storage.nvme_system in loader.yaml.
	//
	// NVMeOnly - hwscan 결과 "SATA/SAS 컨트롤러 없이 NVMe 만으로 구성된
	// 머신" 이면 true. autorender 의 NVMeSystemWanted 가 이 값과 cfg 의
	// 명시적 옵션 (Storage.NVMeSystem) 을 OR 로 합쳐 최종 결정한다.
	//
	// 이 값 자체가 cmdline 을 바꾸는 게 아니라 힌트일 뿐이라, 확실치 않으면
	// false 로 두는 게 안전하다. 사용자는 loader.yaml 의 storage.nvme_system
	// 으로 언제든 강제할 수 있다.
	NVMeOnly bool
}

// DefaultOptions returns the defaults.
//
// LoaderBus is "usb" because this loader only supports USB boot. DSM's 4.4
// kernel only recognises a boot disk as USB (identified by vid/pid on the
// command line) or as a SATA DOM, and only then creates /dev/synoboot*. Put the
// loader on any other bus - virtio-scsi, NVMe - and synoboot never appears, so
// the DSM install fails at the end when it cannot mount the boot partition. A
// SATA DOM costs a SATA port and therefore a bay, so USB it is.
//
// DefaultOptions - 기본값.
//
// LoaderBus 가 "usb" 인 이유: 이 로더는 USB 부팅만 지원한다. DSM 의 4.4 커널은
// 부트 디스크를 USB(cmdline 의 vid/pid 로 식별) 아니면 SATA DOM 으로만 인식해
// /dev/synoboot* 를 만든다. 그 밖의 버스(virtio-scsi, NVMe 등)에 로더를 두면
// synoboot 이 생기지 않고, DSM 설치가 마지막에 부트 파티션을 마운트하지 못해
// 통째로 실패한다. SATA DOM 은 SATA 포트를 하나 잡아먹어 베이가 줄어드므로
// USB 로 고정한다.
func DefaultOptions() Options {
	return Options{EFI: false, LoaderBus: "usb", LoaderSizeMB: 0}
}

// mpt3Platforms are the platforms where the mpt3sas driver actually works. On
// every other DT platform it has to be blacklisted, or a machine with no SAS
// controller stalls during probe.
//
// mpt3Platforms - mpt3sas 드라이버가 제대로 도는 플랫폼들. 나머지 DT
// 플랫폼에서는 반드시 blacklist 해야 SAS 컨트롤러 없는 머신이 probe 중에
// 멈추지 않는다.
var mpt3Platforms = map[string]bool{
	"purley": true, "broadwellnkv2": true, "epyc7002": true, "epyc7003": true,
	"epyc7003ntb": true, "geminilakenk": true, "icelaked": true,
	"r1000nk": true, "v1000nk": true,
}

// i2cI801Platforms have an SMBus controller whose in-kernel initialisation,
// under DSM, either hangs or produces an unusable adapter.
//
// i2cI801Platforms - SMBus 컨트롤러가 있어서, DSM 아래에서 그 in-kernel
// 초기화가 hang 되거나 못 쓰는 어댑터를 만들어내는 플랫폼들.
var i2cI801Platforms = map[string]bool{
	"broadwell": true, "broadwellnk": true, "broadwellnkv2": true,
	"icelaked": true, "purley": true,
}

// igfxOffPlatforms need the iGPU excluded from the IOMMU, otherwise DSM's
// graphics stack and transcoding do not initialise.
//
// igfxOffPlatforms - iGPU 를 IOMMU 에서 빼야 하는 플랫폼들. 안 빼면 DSM 의
// 그래픽 스택과 트랜스코딩이 초기화되지 않는다.
var igfxOffPlatforms = map[string]bool{
	"apollolake": true, "geminilake": true, "geminilakenk": true,
}

// sasModelPlatforms present themselves to DSM as SAS-capable units.
// sasModelPlatforms - DSM 에게 SAS 를 지원하는 유닛으로 자기를 알리는 플랫폼.
var sasModelPlatforms = map[string]bool{"purley": true, "broadwellnkv2": true}

// Build assembles the DSM kernel command line.
//
// The ordering below is deliberate and stable: identity first, then boot
// environment, then platform quirks, then user overrides last so that a user
// entry always wins.
//
// Build - DSM 커널 커맨드라인을 조립한다.
//
// 아래 순서는 의도적이고 고정이다. 정체성이 먼저, 그 다음 부팅 환경, 그
// 다음 플랫폼별 예외, 마지막이 사용자 override 다. 사용자가 적은 값이 항상
// 이기게 하기 위해서다.
func Build(cfg *config.Config, plat *catalog.Platform, identity Identity, opts Options) (*Builder, error) {
	productVer := cfg.ProductVersion()
	if _, ok := plat.Kernels[productVer]; !ok {
		return nil, fmt.Errorf("platform %s has no kernel for DSM %s", plat.Name, productVer)
	}
	kernelMajor := plat.KernelMajor(productVer)

	b := New()

	// --- identity / 정체성 --------------------------------------------------
	b.Set("syno_hw_version", cfg.Model)
	b.Set("sn", identity.Serial)
	b.Set("vid", cfg.Identity.VID)
	b.Set("pid", cfg.Identity.PID)
	for i, mac := range identity.MACs {
		b.Set(fmt.Sprintf("mac%d", i+1), mac)
	}
	b.Set("netif_num", fmt.Sprintf("%d", len(identity.MACs)))

	// skip_vender_mac_interfaces lists the interfaces that should skip the
	// vendor MAC check - the check that mac1..N match the real NICs. Every
	// interface is always listed, so that Synology Assistant does not drop the
	// connection when mac1 differs from the real NIC MAC. The value is the list
	// of interface indices, 0,1,2,...
	//
	// mac1 itself is only DSM's identity MAC, used for QuickConnect, licensing
	// and Assistant matching, and kept by the kernel in
	// /proc/sys/kernel/syno_mac_address1. On its own it does not change the MAC
	// a card puts on the wire. The wire MAC is written directly with
	// SIOCSIFHWADDR by vibeldr-init during the ramdisk stage
	// (cmd/vibeldr-init/hwmac_linux.go), mac1 to eth0, mac2 to eth1 and so on,
	// which is why the addresses written here are also what the router's DHCP
	// list and find.synology show.
	//
	// skip_vender_mac_interfaces 는 "이 인터페이스들은 벤더 MAC 검사(=mac1..N
	// 이 실제 NIC 과 같은지)를 건너뛰라" 는 목록이다. 모든 인터페이스를 항상
	// 넣는다. 그래야 mac1 이 실제 NIC MAC 과 달라도 시놀로지 어시스턴트가
	// 접속을 끊지 않는다. 값은 인터페이스 번호 목록 0,1,2,... 이다.
	//
	// mac1 자체는 DSM 의 "정체성 MAC"(QuickConnect·라이선스·어시스턴트
	// 매칭용, 커널이 /proc/sys/kernel/syno_mac_address1 에 보관)일 뿐이라,
	// 이 값만으로는 랜카드가 선로에서 쓰는 MAC 이 바뀌지 않는다.
	// 선로 MAC 은 vibeldr-init 이 램디스크 단계에서 SIOCSIFHWADDR 로 직접
	// 박는다(cmd/vibeldr-init/hwmac_linux.go). mac1->eth0, mac2->eth1 … 순서로
	// 붙으므로, 공유기 DHCP 목록과 find.synology 에도 여기 적힌 MAC 이 뜬다.
	var skip []string
	for i := range identity.MACs {
		skip = append(skip, fmt.Sprintf("%d", i))
	}
	if len(skip) > 0 {
		b.Set("skip_vender_mac_interfaces", strings.Join(skip, ","))
	}
	b.Set("vender_format_version", "2")

	// --- console and panic behaviour / 콘솔과 패닉 동작 ---------------------
	b.Flag("earlyprintk")
	b.Set("earlycon", "uart8250,io,0x3f8,115200n8")
	// Two consoles are given, and the order matters: the kernel hands the last
	// console= that registered successfully to /dev/console, so with ttyS0
	// listed after tty0 it uses ttyS0 when that exists and falls back to tty0
	// when it does not.
	//
	// tty0 is needed because of the DS918+ (apollolake). On that kernel the
	// 8250 driver counts four ports and nobody claims 0x3f8, so ttyS0 never
	// registers - the SA6400 registers it fine. That leaves no console at all,
	// the kernel prints "unable to open an initial console" and never runs
	// /init, then falls through to root=/dev/md0 with rootwait and waits
	// forever, silent on both the screen and the serial port.
	//
	// The whole of DSM's boot hangs off this device. The command is built into
	// busybox as it stands: /bin/ash /linuxrc.syno > /dev/console 2>&1. If the
	// redirect target will not open, the boot script cannot even start.
	//
	// 콘솔을 둘 준다. 순서가 중요하다. 커널은 마지막으로 "등록에 성공한"
	// console= 을 /dev/console 로 넘겨주므로, 뒤에 적은 ttyS0 가 있으면
	// 그걸 쓰고 없으면 tty0 로 떨어진다.
	//
	// tty0 가 필요한 이유는 DS918+(apollolake) 다. 그 커널은 8250 드라이버가
	// 포트를 4 개 세어놓고 0x3f8 을 아무도 안 잡아서 ttyS0 가 끝내 등록되지
	// 않는다 (SA6400 은 등록된다). 그러면 콘솔이 하나도 없는 상태가 되고,
	// 커널은 "unable to open an initial console" 을 찍은 뒤 /init 을 아예
	// 실행하지 못한다. 그 다음은 root=/dev/md0 + rootwait 로 빠져 영원히
	// 기다린다. 화면도 시리얼도 조용한 채로.
	//
	// DSM 부팅 전체가 이 장치에 매달려 있다. busybox 안에 이 명령이 그대로
	// 박혀 있다: `/bin/ash /linuxrc.syno > /dev/console 2>&1`. 리다이렉트
	// 대상이 안 열리면 부팅 스크립트가 시작조차 못 한다.
	b.Add("console", "tty0")
	b.Add("console", "ttyS0,115200n8")
	// keep_bootcon is not set here, and must never be.
	//
	// On a model where the real console never appears (DS918+) all output
	// vanishes the moment the boot console is switched off, which makes this
	// flag very tempting for debugging. But earlycon's write function lives in
	// .init.text, and free_initmem() releases that region and marks it NX -
	// which is exactly why the kernel switches the boot console off just before
	// free_initmem. Prevent that with keep_bootcon and the next printk tries to
	// execute freed memory and dies immediately:
	//
	//	Freeing unused kernel memory: 936K
	//	kernel tried to execute NX-protected page - exploit attempt? (uid: 0)
	//
	// The DS3622xs+ dies this way. The DS918+ shows no symptom only because it
	// never reaches free_initmem, not because it is safe.
	//
	// keep_bootcon 은 넣지 않는다. 절대 넣지 말 것.
	//
	// 진짜 콘솔이 끝내 안 나타나는 기종(DS918+)에서는 부트콘솔이 꺼지는
	// 순간 출력이 통째로 사라져서, 디버깅용으로 이 플래그가 탐난다.
	// 하지만 earlycon 의 write 함수는 .init.text 에 있고 free_initmem()
	// 이 그 영역을 해제하면서 NX 로 막는다. 커널이 free_initmem 직전에
	// 부트콘솔을 끄는 이유가 바로 이것이다. keep_bootcon 으로 그걸 막으면
	// 그 다음 printk 가 해제된 메모리를 실행하려 들어 즉시 죽는다 (위 로그).
	//
	// DS3622xs+ 가 이 경로로 죽는다. DS918+ 에서 증상이 안 보이는 것은
	// free_initmem 에 도달조차 못 하기 때문이지 안전해서가 아니다.
	//
	// To see the early boot temporarily, write it into loader.yaml's cmdline by
	// hand. It does not go into an image that is meant to reach an install.
	//
	// 부팅 초반만 보려고 일시적으로 켜야 하면 loader.yaml 의 cmdline 에
	// 직접 적는다. 설치까지 갈 이미지에는 넣지 않는다.
	b.Set("consoleblank", fmt.Sprintf("%d", cfg.Boot.ConsoleBlank))
	b.Set("loglevel", "15")
	b.Set("log_buf_len", "32M")
	b.Set("panic", fmt.Sprintf("%d", cfg.Boot.KernelPanic))
	b.Flag("nowatchdog")

	// --- root filesystem / 루트 파일시스템 ----------------------------------
	b.Set("root", "/dev/md0")
	b.Flag("rootwait")

	// --- firmware interface / 펌웨어 인터페이스 -----------------------------
	if opts.EFI {
		b.Flag("withefi")
	} else {
		b.Flag("noefi")
	}

	// --- kernel generation differences / 커널 세대별 차이 -------------------
	if kernelMajor < 5 {
		// Only a loader on SATA is announced as a disk-on-module. With this
		// value set, DSM maps the synoboot partitions on the assumption that
		// the loader is a SATA DOM; if it is really on another bus such as
		// virtio-scsi the mapping never happens, /dev/synoboot* are empty
		// shells, and the DSM install fails mounting the boot partition. When
		// the bus is unknown (empty string) nothing is claimed - the 5.10 line
		// installs fine without this value, so leaving it out is the safe
		// choice when in doubt.
		//
		// SATA 에 붙은 로더만 disk-on-module 로 알린다. 이 값을 주면 DSM 은
		// 로더가 SATA DOM 이라는 전제로 synoboot 파티션을 매핑하는데, 실제로
		// 다른 버스(virtio-scsi 등)에 있으면 매핑이 안 되어 /dev/synoboot* 가
		// 빈 껍데기가 되고 DSM 설치가 부트 파티션 마운트에서 실패한다.
		// 버스를 모르면(빈 문자열) 주장하지 않는다. 5.10 계열은 이 값 없이도
		// 설치가 끝까지 가므로, 모를 때는 넣지 않는 쪽이 안전하다.
		if opts.LoaderBus == "sata" {
			b.Set("synoboot_satadom", fmt.Sprintf("%d", cfg.Boot.SATADOM))
			if opts.LoaderSizeMB > 0 {
				b.Set("dom_szmax", fmt.Sprintf("%d", opts.LoaderSizeMB))
			}
		}
		b.Set("elevator", "elevator")
	} else {
		// 5.10 traps split locks by default, which DSM's own drivers trigger.
		// 5.10 은 split lock 을 기본으로 잡는데, DSM 자체 드라이버가 그걸
		// 유발한다.
		b.Set("split_lock_detect", "off")
	}

	// --- module signatures / 모듈 서명 --------------------------------------
	// Drivers Synology did not sign. / 시놀로지가 서명하지 않은 드라이버.
	//
	// The loader carries several hundred modules for hardware Synology never
	// shipped a driver for, and the kernel refuses every one of them with
	// EKEYREJECTED: it is built to load only modules signed with Synology's
	// key, and nobody outside Synology has that key. The parameter is the
	// kernel's own way of saying the signature is informative rather than
	// binding, and it is the difference between a machine with a network card
	// and a machine without one. On the DSM kernel it only takes effect
	// because the kernel patch releases the boot parameter lock that would
	// otherwise ignore it (internal/kpatch).
	//
	// 로더는 시놀로지가 드라이버를 낸 적 없는 하드웨어용 모듈 수백 개를
	// 싣고 다니는데, 커널은 그 전부를 EKEYREJECTED 로 거부한다. 시놀로지
	// 키로 서명된 모듈만 로드하도록 빌드돼 있고, 그 키는 시놀로지 밖에
	// 아무도 갖고 있지 않기 때문이다. 이 파라미터는 서명을 구속이 아니라
	// 참고로 취급하라는 커널 자신의 표현이고, 랜카드가 있는 기계와 없는
	// 기계를 가르는 차이다. DSM 커널에서는 커널 패치가 부트 파라미터 잠금을
	// 풀어 줘야만 먹힌다. 잠금이 걸려 있으면 이 값은 무시된다
	// (internal/kpatch).
	b.Set("module.sig_enforce", "0")

	// --- disk topology / 디스크 토폴로지 ------------------------------------
	b.Set("HddHotplug", "1")
	if !cfg.Storage.NCQ {
		// See config.Storage.NCQ: a virtual controller that cannot report a
		// failed queued command makes the kernel think the disk is dying.
		//
		// config.Storage.NCQ 참고. 큐잉 실패를 보고하지 못하는 가상
		// 컨트롤러는 커널이 디스크가 죽어 간다고 판단하게 만든다.
		b.Set("libata.force", "noncq")
	}
	if plat.DT {
		b.Set("syno_ttyS0", "serial,0x3f8")
		// syno_ttyS1 names the port DSM's hardware monitor uses to reach the
		// power-management microcontroller. A virtual machine has no such part,
		// and synobios waits for it forever - but leaving the parameter out
		// changes nothing, because the module has the device path built in.
		// That wait is handled where it actually blocks the boot: see the
		// init.post hook in internal/ramdisk.
		//
		// syno_ttyS1 은 DSM 의 하드웨어 모니터가 전원관리 마이크로컨트롤러에
		// 닿는 포트를 지정한다. 가상 머신에는 그런 부품이 없어 synobios 가
		// 영원히 기다리지만, 파라미터를 빼도 달라지는 건 없다. 모듈 안에
		// 장치 경로가 박혀 있기 때문이다. 그 기다림은 실제로 부팅을 막는
		// 자리에서 처리한다. internal/ramdisk 의 init.post 훅을 보라.
		b.Set("syno_ttyS1", "serial,0x2f8")
	} else {
		b.Set("SMBusHddDynamicPower", "1")
		b.Set("syno_hdd_detect", "0")
		b.Set("syno_hdd_powerup_seq", "0")
	}

	// Storage mapping: only one of the two mechanisms (the SataPortMap and
	// DiskIdxMap pair, or sata_remap) is emitted. When the user did not say,
	// it is chosen from the platform class (autorender.go has the rules). The
	// bay count is config.MaxBays (storage.bays, else the model's), and
	// controller ports are mapped into bays only up to that many.
	//
	// 스토리지 매핑: 두 방식 (SataPortMap+DiskIdxMap 쌍 / sata_remap) 중
	// 하나만 방출한다. 사용자가 명시하지 않은 경우 플랫폼 클래스에서 자동
	// 선택한다 (자세한 규칙은 autorender.go 참고). 베이 수는 config.MaxBays
	// (storage.bays, 없으면 모델 값) 이고, 컨트롤러 포트를 거기까지만 베이로
	// 매핑한다.
	policy := SelectStoragePolicy(cfg, plat, opts.Controllers, cfg.MaxBays())
	switch {
	case policy.Portmap != "":
		b.Set("SataPortMap", policy.Portmap)
		b.Set("DiskIdxMap", policy.IdxMap)
	case policy.UseRemap:
		b.Set("sata_remap", policy.Remap)
	}
	// usbasinternal is a bare flag set from storage.usb_as_internal. Nothing in
	// this loader reads it back; it only asks DSM to treat USB disks as
	// internal bays, so if they do not appear as bays, this is the flag to try.
	//
	// usbasinternal 은 storage.usb_as_internal 로 켜는 값 없는 플래그다. 이
	// 로더 안에서는 아무도 이 값을 다시 읽지 않는다. DSM 에게 USB 디스크를
	// 내장 베이로 다뤄 달라고 알릴 뿐이라, USB 디스크가 베이로 안 뜨면 이
	// 플래그를 켜 본다.
	b.FlagIf(cfg.Storage.USBAsInternal, "usbasinternal")

	// nvmesystem: the storage manager in DSM 7.2+ only allows "a system volume
	// may be built on NVMe with no SATA or SAS slot at all" when this flag is
	// present. Without it, an SSD-only machine - a mini PC or an all-NVMe
	// server - fails to create the system partition and the boot stops halfway.
	//
	// It is only set when the user turned it on explicitly in loader.yaml, or
	// when hwscan found no SATA controller at all. On the majority of machines,
	// which do have SATA, nothing is emitted and the command line stays quiet.
	//
	// nvmesystem: DSM 7.2+ 의 storage-manager 는 이 플래그가 있을 때만
	// "SATA/SAS 슬롯이 하나도 없어도 NVMe 로 시스템 볼륨을 만들어도 된다" 를
	// 허용한다. 없으면 SSD-only 머신 (미니 PC 나 완전 NVMe 서버) 에서
	// 시스템 파티션 생성 자체가 실패하고 부팅이 반쯤에서 멈춘다.
	//
	// 사용자가 loader.yaml 에서 명시적으로 켰거나, hwscan 이 SATA 컨트롤러를
	// 하나도 못 찾은 경우에만 켠다 (SATA 가 있는 대다수 머신에서는 방출하지
	// 않아 cmdline 을 조용히 유지한다).
	b.FlagIf(NVMeSystemWanted(cfg, opts), "nvmesystem")

	// sortnetif: on a machine with mixed vendors (Intel plus Realtek, say) the
	// order the cards register in shifts from boot to boot, so the card mac1
	// lands on changes. With a single vendor there is no reason to turn it on,
	// and turning it on anyway rearranges the names, which is its own surprise.
	// So it follows what hwscan found.
	//
	// sortnetif: 벤더 (Intel + Realtek 같은) 가 섞인 머신에서는 부팅마다
	// 카드 등록 순서가 흔들려 mac1 이 붙는 카드가 달라진다. 단일 벤더면
	// 켤 이유가 없고, 실제로 켜면 오히려 이름이 재배치되어 놀라기 쉽다.
	// 그래서 hwscan 결과에 따라 필요할 때만 켠다.
	b.FlagIf(SortnetifWanted(opts.NICProfile, len(identity.MACs)), "sortnetif")

	// --- power and PCI / 전원과 PCI -----------------------------------------
	b.Set("pcie_aspm", "off")
	b.FlagIf(plat.NoFlagsContains("x2apic"), "nox2apic")
	b.SetIf(igfxOffPlatforms[plat.Name], "intel_iommu", "igfx_off")
	b.SetIf(i2cI801Platforms[plat.Name], "initcall_blacklist", "i2c_i801_init")
	b.SetIf(sasModelPlatforms[plat.Name], "SASmodel", "1")

	// --- module blacklist / 모듈 차단 목록 ----------------------------------
	for _, m := range cfg.Modules.Blacklist {
		b.AppendCSV("modprobe.blacklist", m)
	}
	if plat.DT && !mpt3Platforms[plat.Name] {
		b.AppendCSV("modprobe.blacklist", "mpt3sas")
	}

	// --- user overrides, applied last so they win ---------------------------
	// --- 사용자 override. 이기도록 마지막에 적용한다 -------------------------
	keys := make([]string, 0, len(cfg.Cmdline))
	for k := range cfg.Cmdline {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.Set(k, cfg.Cmdline[k])
	}

	return b, nil
}

// Identity is the resolved serial and MAC set for a build. It is passed in
// rather than read from the config because both may be generated.
//
// Identity - 이 빌드에 확정된 시리얼과 MAC 묶음. 둘 다 생성될 수 있는
// 값이라 config 에서 읽지 않고 밖에서 받는다.
type Identity struct {
	Serial string
	MACs   []string
}
