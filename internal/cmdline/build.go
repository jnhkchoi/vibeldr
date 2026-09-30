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
	// Controllers is the disk controller list hwscan sorted into bay order. On
	// a non-DT platform SataPortMap and DiskIdxMap are computed from it. Empty
	// means no computation and a fallback that uses only the bay count.
	//
	// Controllers - hwscan 이 베이 순서로 정렬해 준 디스크 컨트롤러 목록.
	// 비-DT 플랫폼에서 SataPortMap/DiskIdxMap 을 이 구성에서 계산한다.
	// 비어 있으면 계산하지 않고 베이 수만 쓰는 폴백으로 간다.
	Controllers []hwscan.Controller
}

// DefaultOptions returns the options with nothing detected yet.
// DefaultOptions - 아직 아무것도 감지하지 않은 옵션을 돌려준다.
func DefaultOptions() Options {
	return Options{}
}

// i2cI801Platforms have an SMBus controller whose in-kernel initialisation,
// under DSM, either hangs or produces an unusable adapter. Of the three models
// only DS3622xs+ (broadwellnk) builds the driver into its kernel.
//
// i2cI801Platforms - SMBus 컨트롤러가 있어서, DSM 아래에서 그 in-kernel
// 초기화가 hang 되거나 못 쓰는 어댑터를 만들어내는 플랫폼들. 세 모델 중에는
// DS3622xs+ (broadwellnk) 만 커널에 그 드라이버가 들어 있다.
var i2cI801Platforms = map[string]bool{"broadwellnk": true}

// igfxOffPlatforms need the iGPU excluded from the IOMMU, otherwise DSM's
// graphics stack and transcoding do not initialise. Of the three models only
// DS918+ (apollolake) has an iGPU driver.
//
// igfxOffPlatforms - iGPU 를 IOMMU 에서 빼야 하는 플랫폼들. 안 빼면 DSM 의
// 그래픽 스택과 트랜스코딩이 초기화되지 않는다. 세 모델 중에는 DS918+
// (apollolake) 만 iGPU 드라이버가 있다.
var igfxOffPlatforms = map[string]bool{"apollolake": true}

// smbusHddPowerPlatforms read SMBusHddDynamicPower. The DS3622xs+ kernel does;
// the DS918+ kernel and ramdisk have no such name.
//
// smbusHddPowerPlatforms - SMBusHddDynamicPower 를 읽는 플랫폼. DS3622xs+ 커널은
// 읽고, DS918+ 커널과 램디스크에는 그 이름이 없다.
var smbusHddPowerPlatforms = map[string]bool{"broadwellnk": true}

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
	// tty0 is listed because of the DS918+ (apollolake). On that kernel the
	// 8250 driver counts four ports and nobody claims 0x3f8, so ttyS0 never
	// registers - the SA6400 registers it fine. There the serial output stops
	// the moment the boot console is switched off, a few seconds into the
	// boot. The boot itself goes on, and the kernel log keeps filling its
	// buffer (log_buf_len below).
	//
	// The whole of DSM's boot hangs off this device. The command is built into
	// busybox as it stands: /bin/ash /linuxrc.syno > /dev/console 2>&1. If the
	// redirect target will not open, the boot script cannot even start.
	//
	// 콘솔을 둘 준다. 순서가 중요하다. 커널은 마지막으로 "등록에 성공한"
	// console= 을 /dev/console 로 넘겨주므로, 뒤에 적은 ttyS0 가 있으면
	// 그걸 쓰고 없으면 tty0 로 떨어진다.
	//
	// tty0 를 적는 이유는 DS918+(apollolake) 다. 그 커널은 8250 드라이버가
	// 포트를 4 개 세어놓고 0x3f8 을 아무도 안 잡아서 ttyS0 가 끝내 등록되지
	// 않는다 (SA6400 은 등록된다). 거기서는 부트콘솔이 꺼지는 순간, 부팅 몇 초
	// 만에 시리얼 출력이 멈춘다. 부팅 자체는 계속되고, 커널 로그는 버퍼에 계속
	// 쌓인다 (아래 log_buf_len).
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
	// The DS3622xs+ dies this way, and the DS918+ stops booting with it too.
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
	// DS3622xs+ 가 이 경로로 죽고, DS918+ 도 이 플래그가 있으면 부팅이 멈춘다.
	//
	// To see the early boot temporarily, write it into loader.yaml's cmdline by
	// hand. It does not go into an image that is meant to reach an install.
	//
	// 부팅 초반만 보려고 일시적으로 켜야 하면 loader.yaml 의 cmdline 에
	// 직접 적는다. 설치까지 갈 이미지에는 넣지 않는다.
	b.Set("loglevel", "15")
	b.Set("log_buf_len", "32M")
	b.Set("panic", fmt.Sprintf("%d", cfg.Boot.KernelPanic))
	b.Flag("nowatchdog")

	// --- root filesystem / 루트 파일시스템 ----------------------------------
	b.Set("root", "/dev/md0")
	b.Flag("rootwait")

	// Neither withefi nor noefi. The kernel reads only noefi, which switches
	// the EFI runtime services off: a legacy BIOS boot has none to switch off,
	// and a UEFI boot should keep them. withefi is read by nothing.
	//
	// withefi 도 noefi 도 넣지 않는다. 커널이 읽는 것은 EFI 런타임 서비스를 끄는
	// noefi 뿐이다. 레거시 BIOS 부팅에는 끌 서비스가 없고, UEFI 부팅은 서비스를
	// 그대로 써야 한다. withefi 는 아무도 읽지 않는다.

	// --- kernel generation differences / 커널 세대별 차이 -------------------
	if kernelMajor >= 5 {
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
	// HddHotplug is a 4.4 kernel parameter; the 5.10 kernel has no such name.
	// HddHotplug 는 4.4 커널 파라미터다. 5.10 커널에는 이 이름이 없다.
	if kernelMajor < 5 {
		b.Set("HddHotplug", "1")
	}
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
		b.SetIf(smbusHddPowerPlatforms[plat.Name], "SMBusHddDynamicPower", "1")
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
	// --- power and PCI / 전원과 PCI -----------------------------------------
	b.Set("pcie_aspm", "off")
	b.SetIf(igfxOffPlatforms[plat.Name], "intel_iommu", "igfx_off")
	b.SetIf(i2cI801Platforms[plat.Name], "initcall_blacklist", "i2c_i801_init")

	// --- module blacklist / 모듈 차단 목록 ----------------------------------
	for _, m := range cfg.Modules.Blacklist {
		b.AppendCSV("modprobe.blacklist", m)
	}
	// pgdrv is Realtek's test tool driver. The DS3622xs+ and SA6400 ramdisks
	// carry it, and it claims 10ec:8125/8136/8161/8167/8168/8169 - the cards
	// r8168 and r8125 drive. DSM itself only loads it for an out-of-band
	// firmware update, which the loader switches off (support_oob_ctl="no").
	//
	// pgdrv 는 Realtek 의 시험 도구 드라이버다. DS3622xs+ 와 SA6400 램디스크에
	// 들어 있고, 10ec:8125/8136/8161/8167/8168/8169 - r8168·r8125 가 맡는 카드 -
	// 를 자기 것으로 잡는다. DSM 자신은 대역외 펌웨어 갱신 때만 올리는데, 로더가
	// 그것을 끈다 (support_oob_ctl="no").
	b.AppendCSV("modprobe.blacklist", "pgdrv")

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
