// vibeldr-init is the helper that runs once inside the DSM ramdisk and adjusts
// that ramdisk to suit this machine.
//
// It does two things, neither of which the ramdisk can do for itself, because
// both depend on hardware Synology did not build:
//
//   - the PCI addresses baked into the device tree are an appliance's, for this
//     model, so every real disk bay reads as empty. They are rewritten from
//     what sysfs reports.
//
//   - the boot script loads only the drivers on a hard-coded list, so a network
//     card that is not on it never comes up even when its driver is in the
//     ramdisk. Everything matching an attached device is loaded.
//
// The usual way to solve this is to boot a Linux distribution first and run a
// fix-up script on top of it. That is not needed: the ramdisk is already Linux,
// sysfs is already mounted and the drivers are already there. All that is
// missing is something small enough to put inside it.
//
// Built static, with no libc. It never fails a boot. Where it does not know
// what to do it says so on the console and withdraws - a machine that would
// have booted without this helper still boots.
//
// vibeldr-init - DSM 램디스크 안에서 한 번 돌면서, 이 머신에 맞도록
// 램디스크를 손보는 헬퍼.
//
// 두 가지 일을 한다. 둘 다 램디스크 스스로는 못 한다 - 시놀로지가 안 만든
// 하드웨어에 의존하기 때문이다:
//
//   - device tree 에 박힌 PCI 주소는 이 모델 원래 기기의 것이라, 실제 디스크
//     베이가 전부 비어 보인다. sysfs 가 알려주는 대로 다시 쓴다.
//
//   - 부팅 스크립트는 하드코딩된 목록의 드라이버만 로드한다. 그래서 목록에
//     없는 네트워크 카드는 드라이버가 램디스크에 있어도 영영 안 올라온다.
//     붙어 있는 장치와 매칭되는 것은 전부 로드한다.
//
// 통상적인 방식은 이 문제를 풀려고 Linux 배포판을 먼저 하나 부팅해서
// 그 위에서 수정 스크립트를 돌리는 것이다. 필요 없다. 램디스크 자체가 이미
// Linux 이고 sysfs 도 마운트돼 있고 드라이버도 다 있다. 안에 넣을 만큼
// 작은 걸 하나 만들면 된다.
//
// static + no libc 로 빌드한다. 부팅을 절대 실패시키지 않는다. 뭘 해야 할지
// 모르면 콘솔에 남기고 물러난다 - 헬퍼 없이도 부팅됐을 머신은 그대로 부팅된다.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"vibeldr/internal/dtb"
	"vibeldr/internal/hwscan"
	"vibeldr/internal/kmod"
	"vibeldr/internal/ramdisk"
	"vibeldr/internal/synoboot"
)

// dtbPaths are where DSM reads the device tree from. /etc.defaults is a symlink
// to /etc, and linuxrc copies the tree into /var/run before it loads any
// drivers. By the time this runs that copy exists too, so both have to be
// rewritten.
//
// dtbPaths - DSM 이 device tree 를 읽는 위치들. /etc.defaults 는 /etc 로의
// 심볼릭 링크이고, linuxrc 는 드라이버 로드 전에 tree 를 /var/run 으로
// 복사한다. 우리가 돌 때는 그 복사본도 이미 있으므로 둘 다 다시 써야 한다.
var dtbPaths = []string{"/etc/model.dtb", "/var/run/model.dtb"}

// newRoot is where DSM's last ramdisk script mounts the installed system.
// Before the pivot, this becomes the new root. Every other mount left on the
// old root is to be cleared away.
//
// newRoot - DSM 의 마지막 램디스크 스크립트가 설치된 시스템을 마운트하는
// 자리. pivot 하기 전에 여기가 새 루트가 된다. 이전 루트에 붙어 있던 다른
// 마운트는 전부 정리 대상이다.
const newRoot = "/tmpRoot"

// defaultLabel is the volume label the image builder puts on the loader's first
// partition. It can be overridden from the kernel command line, so an image
// built with a different label still recognises itself.
//
// defaultLabel - 이미지 빌더가 로더의 첫 파티션에 심는 볼륨 라벨. 커널
// 커맨드라인으로 덮어쓸 수 있어서, 라벨을 바꾼 이미지도 자기를 인식한다.
const defaultLabel = "VIBELDR1"

const (
	labelKey      = "vibeldr_label"
	noDriversFlag = "vibeldr_nodrivers"
	shellFlag     = "vibeldr_shell"
	// consoleTTY is where the loader sends its own console output. The boot script
	// puts a login on another port, which a VM usually does not have, and without
	// this there would be no way in.
	//
	// consoleTTY - 로더가 자신의 콘솔 출력을 내보내는 곳. 부팅 스크립트는
	// 다른 포트에서 로그인을 띄우는데, VM 에는 대개 그 포트가 없어서 이게
	// 없으면 안으로 들어갈 방법이 없다.
	consoleTTY = "/dev/ttyS0"
)

// bootstrapModules are the modules to load by name before any matching can
// find anything. Until these are in, the devices the rest would match against
// are not visible.
//
// bootstrapModules - 매칭으로 찾기 전에 이름으로 먼저 로드해야 하는 모듈들.
// 이것들이 로드돼야 나머지가 매칭할 대상(장치) 이 보이기 시작한다.
var bootstrapModules = []string{
	// The bus the loader is usually plugged into, and the driver that turns a
	// stick on that bus into a disk.
	//
	// 로더가 보통 꽂혀 있는 버스, 그리고 그 버스 위의 스틱을 디스크로
	// 만들어주는 드라이버.
	"usb_common", "usbcore", "xhci_hcd", "xhci_pci", "ehci_hcd", "ehci_pci",
	"usb_storage", "uas",
	// And the filesystem that disk is formatted with. Without it the kernel
	// answers a mount with EINVAL, which reads like a bad argument but means
	// "never heard of that filesystem".
	//
	// 그리고 그 디스크가 포맷된 파일시스템. 이게 없으면 커널이 mount 에
	// EINVAL 을 돌려주는데, 잘못된 인자처럼 읽히지만 실제로는 "그런
	// 파일시스템 처음 본다" 라는 뜻이다.
	"fat", "vfat", "nls_cp437", "nls_iso8859_1", "nls_utf8",
	// On a hypervisor the loader disk sits behind virtio. These are not in the
	// Synology ramdisk, so they are put in at build time - see
	// RamdiskBootstrapModules in `internal/catalog`.
	//
	// 하이퍼바이저 위에서는 로더 디스크가 virtio 뒤에 있다. 시놀로지
	// 램디스크에는 없는 것들이라 빌드할 때 따로 넣어 둔다
	// (`internal/catalog` 의 RamdiskBootstrapModules).
	"virtio", "virtio_ring", "virtio_pci", "virtio_scsi", "virtio_blk",
	"virtio_net",
}

// bootDeviceWait is how long to wait for the loader's disk to be registered,
// and bootDeviceRound how long one look takes before the drivers are given
// another push.
//
// A USB stick is enumerated long after the boot script starts, so asking once
// and giving up would find nothing at all. How long "long" is varies more than
// it looks: on one machine the xHCI controller was up 4.9 seconds in, and on
// the same machine with the serial port taken away it was 16.9 seconds, because
// DSM reached its own "Insert basic USB modules" step much later. Twenty
// seconds would be enough for the first and not for the second, and the failure is
// total - without its own disk the loader has no driver pack, no settings and
// no way to finish. The wait is therefore set far beyond any observed
// enumeration, since it costs nothing on a machine where the disk is already
// there.
//
// bootDeviceWait - 로더의 디스크가 등록될 때까지 기다리는 시간.
// bootDeviceRound - 한 회차의 길이. 그때마다 드라이버를 다시 밀어준다.
//
// USB 스틱은 부팅 스크립트 시작 훨씬 뒤에 열거되니, 한 번 물어보고 포기하면
// 아무것도 못 찾는다. 그 "훨씬 뒤" 가 생각보다 들쭉날쭉하다. 같은 기계에서
// xHCI 컨트롤러가 4.9 초에 올라온 적도 있고, 시리얼 포트를 뗐더니 16.9 초에
// 올라온 적도 있다. DSM 이 자기 "Insert basic USB modules" 단계에 훨씬 늦게
// 도달했기 때문이다. 20 초는 앞의 경우엔 충분하고 뒤의 경우엔 모자란다.
// 실패하면 전부를 잃는다. 자기 디스크가 없으면 드라이버 팩도 설정도 없고
// 마무리할 방법도 없다. 그래서 관측된 어떤 열거 시점보다도 넉넉히 잡는다.
// 이미 디스크가 있는 기계에서는 이 값이 아무 비용도 아니다.
const (
	bootDeviceWait  = 90 * time.Second
	bootDeviceRound = 5 * time.Second
)

func main() {
	// Before anything opens a file. A descriptor the kernel left closed would
	// otherwise be taken by that file, and the output of this program would go
	// into it - see stdio_linux.go.
	//
	// 무엇이든 파일을 열기 전에. 커널이 닫아 둔 서술자가 있으면 그 파일이
	// 그 자리를 차지하고, 이 프로그램의 출력이 그리로 들어간다
	// (stdio_linux.go 참고).
	ensureStdio()
	// The entry points. What has to be done differs per boot stage, and one run
	// cannot cover them all, so argv says which stage this is.
	//
	//   (no argument)  from the ramdisk boot script, before DSM looks for disks.
	//                  Bay mapping and driver loading.
	//   -agent         from systemd inside DSM, after the storage service.
	//   -detach        from the last ramdisk script, right before the unmounts.
	//   -pivot         in place of switch_root, as PID 1.
	//   -stage2        from /etc/rc, once DSM has entered its service stage. A
	//                  shell started before that dies in the transition, and a
	//                  shell holding the console leaves /dev busy, which fails it.
	//   -daemon        started by -stage2 in a session of its own; the
	//                  long-running part.
	//
	// 진입점 분기. 부팅 각 단계마다 해야 하는 일이 달라서 하나의 실행에서
	// 다 하지 못하고, argv 로 어느 단계인지 알려준다.
	//
	//   (인자 없음)    램디스크 부팅 스크립트에서, DSM 이 디스크를 찾기 전에.
	//                  베이 매핑과 드라이버 로드.
	//   -agent         DSM 안의 systemd 에서, 스토리지 서비스 뒤에.
	//   -detach        마지막 램디스크 스크립트에서, 언마운트 직전에.
	//   -pivot         switch_root 대신 PID 1 로.
	//   -stage2        DSM 이 서비스 스테이지에 들어간 뒤 /etc/rc 에서. 그 전에
	//                  띄운 셸은 전환 중에 죽고, 콘솔을 잡은 셸은 /dev 를 바쁘게
	//                  만들어 전환을 실패시킨다.
	//   -daemon        -stage2 가 자기 세션으로 띄우는 장기 실행 파트.
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-pivot":
			// The last ramdisk script execs this instead of switch_root. The helper
			// becomes PID 1, tries the root move itself, and logs the kernel's reason if
			// it is refused.
			//
			// 마지막 램디스크 스크립트가 switch_root 대신 이걸 exec 한다.
			// 헬퍼가 PID 1 이 되어 root 이동을 직접 시도하고, 커널이 거부하면
			// 그 이유를 로그에 남긴다.
			pivotRoot(newRoot, "/sbin/init")
			return
		case "-agent":
			// Started from systemd inside the installed system, after the storage service
			// has finished judging the drives.
			//
			// 설치된 시스템 안의 systemd 에서 시작한다. 스토리지 서비스가
			// 드라이브에 대한 판정을 마친 뒤다.
			if err := runAgent(); err != nil {
				logf("agent: %v", err)
			}
			return
		case "-detach":
			// Called from the last ramdisk script, one line before the pivot.
			// 마지막 램디스크 스크립트에서, pivot 한 줄 앞에 호출된다.
			announce()
			detachDev()
			// The last point in the ramdisk where the loader disk can still be
			// opened. After the pivot it is gone, so the copy of the log has to
			// be written here.
			//
			// 램디스크에서 로더 디스크를 아직 열 수 있는 마지막 지점.
			// pivot 뒤에는 사라지므로 로그 사본은 여기서 써야 한다.
			flushLog("ramdisk")
			return
		case "-stage2":
			// Started from /etc/rc once DSM has entered its service stage. Only the
			// long-running part is started and this returns at once, so the boot is not
			// held up.
			//
			// DSM 이 서비스 스테이지에 진입한 뒤 /etc/rc 에서 시작한다.
			// 장기 실행 파트만 띄우고 바로 return 해서 부팅을 붙잡지 않는다.
			if err := spawnStage2(); err != nil {
				logf("stage 2: %v", err)
			}
			return
		case "-daemon":
			// The long-running part, in a session of its own. It watches the install and,
			// if asked, brings up the rescue console.
			//
			// 자기 세션에서 도는 장기 실행 파트. 인스톨을 감시하고,
			// 요청이 있으면 구조용 콘솔을 띄운다.
			if err := runDaemon(); err != nil {
				logf("stage 2: %v", err)
			}
			return
		}
	}

	// Order matters here, even though no step is allowed to fail the boot.
	// Drivers come first because the rest depends on hardware that only
	// appears once its driver is loaded - most of all the loader's own disk,
	// which on a USB stick does not exist until usb-storage is in.
	//
	// 어느 단계도 부팅을 실패시킬 수 없지만 순서는 중요하다. 드라이버가 먼저다.
	// 나머지는 드라이버가 올라와야 보이는 하드웨어에 기대기 때문이고, 무엇보다
	// 로더 자신의 디스크가 그렇다. USB 스틱이면 usb-storage 가 들어오기 전에는
	// 존재하지 않는다.
	announce()
	// The outbound notification that a boot has started. A failure has no effect
	// on the boot.
	//
	// 외부 알림: 부팅 시작. 실패해도 부팅에 영향이 없다.
	_ = notify("boot_started", nil)
	if hasCmdlineFlag(noDriversFlag) {
		logf("drivers: skipped, %s is set", noDriversFlag)
	} else if err := loadDrivers(); err != nil {
		logf("drivers: %v", err)
		_ = notify("error", map[string]string{"stage": "drivers", "message": err.Error()})
	}
	if err := makeBootDevice(); err != nil {
		logf("boot device: %v", err)
		_ = notify("error", map[string]string{"stage": "boot_device", "message": err.Error()})
	}
	// The disk has a name, so the log has somewhere to go. Written here as well
	// as at the end, because a boot that stops in between would otherwise leave
	// nothing behind - and with no serial port there is no other record at all.
	//
	// 디스크에 이름이 붙었으니 로그를 쓸 곳이 생겼다. 끝에서도 쓰지만 여기서도
	// 쓴다. 그 사이에서 멈추는 부팅은 아무것도 안 남기기 때문이고, 시리얼
	// 포트가 없으면 다른 기록이 아예 없다.
	flushLog("early")
	// The loader's own disk has a name now, so the drivers stacked on it - for
	// hardware Synology ships no driver for - are reachable too.
	//
	// 로더 자신의 디스크가 이름을 얻었으니, 그 위에 얹어둔 드라이버들
	// (시놀로지가 드라이버를 배포하지 않는 하드웨어용) 도 이제 도달 가능하다.
	if !hasCmdlineFlag(noDriversFlag) {
		loadExtraDrivers()
	}
	// Every card is up now, so the chosen MACs go in. This is before DSM brings
	// the network up, so the first address the router ever sees is the one chosen
	// here.
	//
	// 랜카드가 다 올라온 지금 지정 MAC 을 박는다. DSM 이 네트워크를 세우기
	// 전이라, 공유기가 처음 보는 주소가 곧 지정한 주소가 된다.
	applyHardwareMACs()
	// Read the drive model names now. At the pivot there is no /sys.
	// 드라이브 모델명을 지금 읽어 둔다. pivot 때는 /sys 가 없다.
	saveDriveModels()
	if err := mapDiskBays(); err != nil {
		logf("disk bays: %v", err)
		logf("leaving the device tree untouched")
	}
	// Written now as well as at the detach stage, because a boot that stops
	// between the two would otherwise leave nothing behind.
	//
	// detach 단계에서도 쓰지만 여기서도 쓴다. 그 사이에서 멈추는 부팅은
	// 아무것도 안 남기기 때문이다.
	flushLog("boot")
}

// announce prints the running loader's build fingerprint to the console.
//
// It reads the fingerprint planted in the ramdisk during the patch stage. It
// has to be possible to tell "fixed it and it still fails" from "the fix never
// got loaded" from the boot log alone, and since what to do next differs
// completely between those two, this one line is what makes the distinction.
//
// announce - 지금 도는 로더의 빌드 지문을 콘솔에 찍는다.
//
// patch 단계에서 램디스크 안에 심어둔 fingerprint 를 읽는다. 부팅 로그만
// 보고도 "고쳤는데 안 됨" 과 "고친 게 안 실렸음" 을 구분할 수 있어야 하고,
// 이 둘의 다음 조치가 완전히 다르기 때문에 이 한 줄이 그 구분을 만든다.
func announce() {
	id, err := os.ReadFile("/" + ramdisk.BuildIDName)
	if err != nil {
		logf("build: unknown")
		return
	}
	logf("build %s", strings.TrimSpace(string(id)))
}

// logf prints one line to the DSM console.
//
// Messages here are English only. The Synology kernel console cannot render
// Hangul, so a Korean string arrives as broken bytes and the one line that
// explains a stuck boot becomes unreadable. The GRUB banner is English for
// the same reason. Comments are not subject to this - only printed output.
//
// logf - DSM 콘솔에 한 줄 찍는다.
//
// 메시지는 반드시 영문으로 쓴다. 시놀로지 커널 콘솔은 한글을 못 찍어서
// 깨진 바이트가 나오고, 부팅이 막혔을 때 유일한 단서인 이 줄을 못 읽게
// 된다 (GRUB 배너도 같은 이유로 영문이다). 이 제약은 출력에만 걸리고
// 주석에는 걸리지 않는다.
func logf(format string, args ...any) {
	line := "vibeldr: " + fmt.Sprintf(format, args...)
	fmt.Println(line)
	// The same line is kept for the copy on the loader partition. Without a
	// serial port the console swallows everything, and that file is then the
	// only record of the boot - see logfile_linux.go.
	//
	// 같은 줄을 로더 파티션 사본용으로 보관한다. 시리얼 포트가 없으면
	// 콘솔이 전부 삼켜버려서, 그 파일이 부팅의 유일한 기록이 된다
	// (logfile_linux.go 참고).
	keepLine(line)
}

// makeBootDevice attaches the name the DSM updater looks for, /dev/synoboot,
// to the loader's own disk.
//
// Without it the install runs all the way to its last step and fails there with
// "failed to mount boot device /dev/synoboot2". It is done once here and again
// at the start of the installer stage, because /dev can be remounted in between
// and the earlier node may not survive.
//
// makeBootDevice - DSM 업데이터가 찾는 이름(/dev/synoboot) 을 로더의
// 자기 디스크에 붙여준다.
//
// 이게 없으면 인스톨은 끝까지 진행되다가 마지막 단계에서
// "failed to mount boot device /dev/synoboot2" 로 실패한다. 여기서 한 번,
// 인스톨러 스테이지 시작할 때 다시 한 번 수행한다. /dev 가 그 사이에
// 리마운트돼서 이전 노드가 살아남지 못할 수 있기 때문이다.
func makeBootDevice() error {
	deadline := time.Now().Add(bootDeviceWait)
	var last error
	for round := 1; ; round++ {
		res, err := synoboot.CreateWithin(synoboot.DefaultSysBlock, hwscan.DefaultDev, loaderLabel(), bootDeviceRound)
		if err == nil {
			logf("boot device: %s", res)
			return nil
		}
		last = err
		if !time.Now().Before(deadline) {
			return last
		}
		// The disk is not there yet. Which devices were looked at is part of
		// the error, so the log says whether the USB bus exists at all rather
		// than only that the label was not found.
		//
		// 아직 없다. 어떤 장치를 봤는지가 오류에 담겨 있어서, 라벨을 못 찾았다는
		// 것만이 아니라 USB 버스가 있기는 한지가 로그에 남는다.
		logf("boot device: %d: %v", round, err)
		// Push the host controller and storage drivers again. The first
		// attempt can land before the controller's bus exists, and DSM loads
		// its own copies at a moment that moves from boot to boot.
		//
		// 호스트 컨트롤러와 저장장치 드라이버를 다시 밀어 넣는다. 첫 시도가
		// 버스가 생기기 전에 끝났을 수 있고, DSM 이 자기 것을 올리는 시점은
		// 부팅마다 달라진다.
		if added := loadBootstrapModules(); len(added) > 0 {
			logf("boot device: %d: loaded %s", round, strings.Join(added, " "))
		}
	}
}

// loadBootstrapModules puts in the drivers the loader's own disk sits behind,
// and reports which ones went in this time.
//
// They match no device until there is a device for them to match, so they are
// asked for by name rather than found by scanning. Ones already in are skipped
// by the kernel and reported as nothing new, which is what makes this safe to
// call again while waiting.
//
// loadBootstrapModules - 로더 자신의 디스크가 그 뒤에 있는 드라이버들을 넣고,
// 이번에 실제로 들어간 것을 돌려준다.
//
// 이들은 매칭할 장치가 생기기 전까지는 아무 장치와도 매칭되지 않으므로,
// 스캔으로 찾지 않고 이름으로 지명해서 넣는다. 이미 올라온 것은 커널이
// 건너뛰고 새로 들어간 게 없다고 답하므로, 기다리는 동안 몇 번을 다시 불러도
// 안전하다.
func loadBootstrapModules() []string {
	index, err := kmod.Scan(kmod.SearchDirs...)
	if err != nil {
		return nil
	}
	var added []string
	for _, m := range index.ByName(bootstrapModules...) {
		if err := kmod.Load(m); err == nil {
			added = append(added, m.Name)
		}
	}
	return added
}

func loaderLabel() string { return cmdlineValue(labelKey, defaultLabel) }

// mapDiskBays points the model's own device tree at this machine's controllers.
// mapDiskBays - 모델 자체의 device tree 가 이 머신의 컨트롤러를 가리키게 한다.
func mapDiskBays() error {
	controllers, err := hwscan.ScanSysfs()
	if err != nil {
		return fmt.Errorf("scanning for SATA controllers: %w", err)
	}
	logf("found %s", hwscan.Summary(controllers))

	if ctrl, port, ok := hwscan.FindLoaderPort(controllers, hwscan.DefaultDev, loaderLabel()); ok {
		logf("loader disk is on %s port %d; leaving that port out", ctrl, port)
		controllers = hwscan.ExcludePort(controllers, ctrl, port)
	} else {
		logf("loader disk is not on a SATA port, so every port is available")
	}

	ports := bayOrder(controllers)
	// An order set by the user in the wizard wins. With none, the detection order
	// is used as it is. A plan only holds as many bays as the model admits, so
	// spare ports on a machine with more of them never leak in as bays.
	//
	// 사용자가 마법사에서 정한 순서가 있으면 그게 우선이다. 없으면 감지
	// 순서를 그대로 쓴다. 계획에는 모델이 인정하는 베이 수만큼만 들어
	// 있으므로, 포트가 더 많은 기계에서도 여분이 베이로 새지 않는다.
	if plan := loadBayPlan(); len(plan) > 0 {
		ports = applyBayPlan(plan, ports)
		logf("bay plan: %d port(s) placed in the chosen order", len(ports))
	}
	if len(ports) == 0 {
		return fmt.Errorf("no SATA ports left to map")
	}

	// Leave the result where it can still be found after the pivot. The installed
	// system has its own copy of the device tree, and by then sysfs is gone too,
	// so what was worked out here is carried over rather than rediscovered.
	//
	// pivot 뒤에서도 찾을 수 있는 자리에 결과를 남긴다. 설치된 시스템은
	// 자기 device tree 복사본을 갖고 있고, 그때는 sysfs 도 이미 없다.
	// 여기서 알아낸 걸 재발견 대신 실어 나른다.
	if err := savePorts(ports); err != nil {
		logf("ports: %v", err)
	}

	written := 0
	for _, path := range dtbPaths {
		ok, err := rewrite(path, ports)
		if err != nil {
			logf("%s: %v", path, err)
			continue
		}
		if ok {
			written++
		}
	}
	if written == 0 {
		return fmt.Errorf("no device tree could be rewritten")
	}
	return nil
}

// bayOrder flattens the controllers into bay order.
//
// Controllers with disks attached are sorted first, so the first disk lands in
// bay 1 rather than behind six empty bays under a chipset controller nobody
// uses. Within a controller the ports keep their hardware order. Grouping by
// controller rather than by port is explained in the comments on
// hwscan.OrderForBays - it is what keeps the numbering from shuffling across a
// reboot.
//
// bayOrder - 컨트롤러들을 베이 순서에 맞게 편다.
//
// 디스크가 붙어 있는 컨트롤러가 먼저 오도록 정렬한다. 그래야 첫 디스크가
// 아무도 안 쓰는 칩셋 컨트롤러 밑의 빈 베이 6 개 뒤가 아니라 베이 1
// 자리로 온다. 컨트롤러 안에서 포트는 하드웨어 순서 그대로다.
// 그룹핑을 포트가 아니라 컨트롤러 단위로 하는 이유는 hwscan.OrderForBays
// 주석 참고 (재부팅해도 번호가 안 뒤바뀌게 만드는 핵심).
func bayOrder(cs []hwscan.Controller) []dtb.SATAPort {
	var out []dtb.SATAPort
	for _, c := range hwscan.OrderForBays(cs) {
		// SATA only. A device tree's sata slot points at a port through a
		// pcie_root and ata_port pair, and a SAS or RAID HBA has no such thing as an
		// ata_port. Attaching a slot there puts a bay that no disk can ever match into
		// an early position and pushes the real disks back. DSM enumerates the HBA's
		// disks directly, at the SCSI layer.
		//
		// SATA 만. 디바이스 트리의 sata 슬롯은 pcie_root + ata_port 쌍으로
		// 포트를 가리키는데, SAS/RAID HBA 에는 ata_port 라는 게 없다. 거기에
		// 슬롯을 붙이면 아무 디스크도 안 걸리는 베이만 앞자리를 차지해
		// 진짜 디스크가 뒤로 밀린다. HBA 쪽 디스크는 DSM 이 SCSI 계층에서
		// 직접 열거한다.
		if c.Kind != hwscan.KindSATA {
			continue
		}
		for _, p := range c.Ports {
			out = append(out, dtb.SATAPort{PCIeRoot: c.PCIeRoot, Port: p.Index})
		}
	}
	return out
}

func rewrite(path string, ports []dtb.SATAPort) (bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	tree, err := dtb.Parse(raw)
	if err != nil {
		return false, err
	}
	remap, err := tree.ApplySATAPorts(ports)
	if err != nil {
		return false, err
	}
	out, err := tree.Bytes()
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return false, err
	}
	logf("%s: %s", path, remap.Summary())
	return true, nil
}

// loadDrivers loads every module in the ramdisk that claims to handle a device
// actually attached.
//
// It loads only modules already in the ramdisk: Synology's own, plus the few
// the build adds so the loader's disk can be seen (RamdiskBootstrapModules in
// internal/catalog). Nothing is downloaded and nothing is built. On a VM this
// usually brings up one module - a network card the boot script does not list
// - and without it the installer comes up with no network.
//
// loadDrivers - 램디스크 안에 있는 모듈 중 실제로 붙어 있는 장치를
// 담당한다고 하는 것들을 다 로드한다.
//
// 램디스크에 이미 있는 모듈만 로드한다. 시놀로지 것과, 로더 디스크가 보이게
// 빌드 때 넣는 몇 개 (internal/catalog 의 RamdiskBootstrapModules) 다.
// 다운로드도 빌드도 없다. VM
// 에서는 보통 모듈 하나 (부팅 스크립트에 안 적힌 네트워크 카드) 가 여기서
// 뜨고, 이게 없으면 인스톨러가 네트워크 없이 뜬다.
func loadDrivers() error {
	index, err := kmod.Scan(kmod.SearchDirs...)
	if err != nil {
		return err
	}
	aliases, err := kmod.DeviceAliases(hwscan.DefaultSysfs)
	if err != nil {
		return err
	}
	loaded := kmod.Loaded("/proc/modules")

	// The loader's own disk is behind these, and they match no device until
	// there is a device for them to match. Asking by name is what makes the
	// disk appear - and the disk is where the rest of the drivers live.
	//
	// 로더 자신의 디스크가 이것들 뒤에 있고, 이것들은 매칭할 장치가 생기기
	// 전까지 아무 장치와도 매칭되지 않는다. 이름으로 지명해야 디스크가
	// 나타나고, 나머지 드라이버는 그 디스크에 있다.
	order := index.ByName(bootstrapModules...)
	order = append(order, index.Resolve(aliases, loaded)...)
	if len(order) == 0 {
		logf("drivers: %d modules available, %d devices, nothing to add", len(index), len(aliases))
		return nil
	}

	var ok, failed []string
	seen := map[string]bool{}
	for _, m := range order {
		if seen[m.Name] {
			continue
		}
		seen[m.Name] = true
		if err := kmod.Load(m); err != nil {
			// The reason has to travel with the name. "module X failed" does
			// not separate a rejected signature from a missing symbol from a
			// missing file, and those three have nothing in common to try next.
			//
			// 실패 사유를 이름과 함께 남긴다. 이름만 있으면 서명 거부와 심벌
			// 불일치와 파일 없음이 구분되지 않는데, 셋은 다음 조치가 전혀 다르다.
			failed = append(failed, fmt.Sprintf("%s(%v)", m.Name, err))
			continue
		}
		ok = append(ok, m.Name)
	}
	if len(ok) > 0 {
		logf("drivers: loaded %s", strings.Join(ok, " "))
	}
	if len(failed) > 0 {
		logf("drivers: could not load %s", strings.Join(failed, " "))
	}
	return nil
}

// cmdlineOption reads one setting off the kernel command line.
//
// Both "name" and "name=value" count as present. That keeps the two ways of
// writing it - leaving an option on in the GRUB menu, and editing that line to
// give it a value - from meaning different things.
//
// cmdlineOption - 커널 커맨드라인에서 설정 하나를 읽어온다.
//
// "name" 도 "name=value" 도 모두 "존재함" 으로 취급한다. GRUB 메뉴에서
// 옵션을 켜두는 표기와 그 줄을 편집해 값을 지정하는 표기가 서로 다른 뜻으로
// 갈리지 않게 하기 위함이다.
func cmdlineOption(name string) (value string, present bool) {
	for _, field := range cmdlineFields() {
		switch {
		case field == name:
			return "", true
		case strings.HasPrefix(field, name+"="):
			return strings.TrimPrefix(field, name+"="), true
		}
	}
	return "", false
}

// cmdlineValue returns a setting's value, or the fallback when it is absent or
// present as a bare name with no value.
//
// cmdlineValue - 설정의 값을 돌려준다. 없거나 값 없이 이름만 있으면 fallback.
func cmdlineValue(name, fallback string) string {
	if v, ok := cmdlineOption(name); ok && v != "" {
		return v
	}
	return fallback
}

func hasCmdlineFlag(name string) bool {
	_, ok := cmdlineOption(name)
	return ok
}

func cmdlineFields() []string {
	b, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return nil
	}
	return strings.Fields(string(b))
}

// portsFile carries the bay order from the stage that can see the hardware to
// the stage that can reach the installed system.
//
// portsFile - 하드웨어를 볼 수 있는 단계에서 설치된 시스템에 닿을 수 있는
// 단계로 베이 순서를 실어 나른다.
const portsFile = "/vibeldr-ports"

// bayPlanFile is the bay order the user set themselves. The loader plants it
// when it builds the ramdisk, and it is read from here. With no file, the
// detected order is used as it is.
//
// The format is the same as portsFile: one "<PCIe path> <port number>" per
// line, and the order they are written in is bay 1, 2, 3 and so on.
//
// bayPlanFile - 사용자가 직접 정한 베이 순서. 로더가 램디스크를 만들 때
// 심어두면 여기서 읽는다. 없으면 감지한 순서를 그대로 쓴다.
//
// 형식은 portsFile 과 같다. 한 줄에 "<PCIe 경로> <포트 번호>" 하나씩이고,
// 적힌 순서가 곧 베이 1, 2, 3 … 이다.
const bayPlanFile = "/vibeldr-bay-plan"

// loadBayPlan reads the bay order the user set, returning nil when there is no
// file.
//
// loadBayPlan - 사용자가 정한 베이 순서를 읽는다. 파일이 없으면 nil.
func loadBayPlan() []dtb.SATAPort {
	raw, err := os.ReadFile(bayPlanFile)
	if err != nil {
		return nil
	}
	return parsePortLines(string(raw))
}

// applyBayPlan returns only the detected ports that are in the plan, in the
// plan's order.
//
// A port not in the plan is not used: either the user left that place empty, or
// it is past the model's bay count.
//
// A port written into the plan but absent from this machine drops out, and the
// bays after it move forward by that much. Moving the loader to another machine
// therefore changes the numbering. Leaving a gap instead would need a way to
// express "empty bay" on the dtb side, and there is none yet.
//
// applyBayPlan - 감지된 포트 중 계획에 있는 것만, 계획 순서대로 돌려준다.
//
// 계획에 없는 포트는 쓰지 않는다 - 사용자가 "비움" 으로 둔 자리이거나
// 모델의 베이 수 밖이다.
//
// 계획에 적혔지만 이 기계에 없는 포트는 빠지고, 그만큼 뒤 베이가 앞으로
// 당겨진다. 로더를 다른 기계에 옮겨 꽂으면 번호가 달라진다는 뜻이다.
// 빈자리를 남기려면 dtb 쪽에 "빈 베이" 표현이 필요한데 아직 없다.
func applyBayPlan(plan, found []dtb.SATAPort) []dtb.SATAPort {
	if len(plan) == 0 {
		return found
	}
	have := make(map[string]bool, len(found))
	for _, p := range found {
		have[portKey(p)] = true
	}
	var out []dtb.SATAPort
	for _, p := range plan {
		if have[portKey(p)] {
			out = append(out, p)
		}
	}
	return out
}

// portKey is the string identifying one port.
// portKey - 포트 하나를 식별하는 문자열.
func portKey(p dtb.SATAPort) string { return fmt.Sprintf("%s/%d", p.PCIeRoot, p.Port) }

// parsePortLines reads "<PCIe path> <port number>" lines.
// parsePortLines - "<PCIe 경로> <포트 번호>" 줄들을 읽는다.
func parsePortLines(text string) []dtb.SATAPort {
	var out []dtb.SATAPort
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		n, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		out = append(out, dtb.SATAPort{PCIeRoot: f[0], Port: uint32(n)})
	}
	return out
}

func savePorts(ports []dtb.SATAPort) error {
	var b strings.Builder
	for _, p := range ports {
		fmt.Fprintf(&b, "%s %d\n", p.PCIeRoot, p.Port)
	}
	return os.WriteFile(portsFile, []byte(b.String()), 0o644)
}

func loadPorts() ([]dtb.SATAPort, error) {
	raw, err := os.ReadFile(portsFile)
	if err != nil {
		return nil, err
	}
	var out []dtb.SATAPort
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 {
			continue
		}
		n, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		out = append(out, dtb.SATAPort{PCIeRoot: f[0], Port: uint32(n)})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s has no ports in it", portsFile)
	}
	return out, nil
}

// mapInstalledSystem points the installed system's device tree at this
// machine's disk controllers.
//
// Rewriting the ramdisk's copy is not enough. Once DSM moves into the system on
// the disk it reads that system's own tree, which came from the .pat and
// describes the controllers of a real appliance. Without this the bays are
// there and every one of them is empty.
//
// mapInstalledSystem - 설치된 시스템의 device tree 가 이 머신의 디스크
// 컨트롤러를 가리키게 한다.
//
// 램디스크 쪽 사본만 고쳐서는 모자란다. DSM 이 디스크의 시스템으로 옮겨 가면
// 그 시스템 자신의 tree 를 읽는데, 그건 .pat 에서 왔고 실제 기기의 컨트롤러를
// 기술한다. 이게 없으면 베이는 있는데 전부 비어 있다.
func mapInstalledSystem(root string) {
	ports, err := loadPorts()
	if err != nil {
		logf("installed system: %v", err)
		return
	}
	var written int
	for _, rel := range []string{"/etc/model.dtb", "/etc.defaults/model.dtb"} {
		path := root + rel
		ok, err := rewrite(path, ports)
		if err != nil {
			logf("installed system: %s: %v", path, err)
			continue
		}
		if ok {
			logf("installed system: %s: %d bay(s) mapped", path, len(ports))
			written++
		}
	}
	if written == 0 {
		logf("installed system: no device tree was rewritten; DSM will show no disks")
	}
}
