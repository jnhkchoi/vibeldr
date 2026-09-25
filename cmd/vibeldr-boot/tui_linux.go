//go:build linux

// tui_linux.go is the text menu and the build pipeline.
//
// Where there is a screen, wizard_linux.go's graphical wizard comes up first;
// with no framebuffer, or one that will not open, the boot lands on this menu
// instead. It uses nothing but text scrolling by a line at a time, so that it
// reads the same over a serial console. Clearing the screen is one VT100 escape
// throughout (\x1b[H\x1b[2J).
//
// A menu item is chosen by number and q goes back. Free text is typed straight
// after the prompt.
//
// buildFlow, which actually downloads DSM, patches it and writes it to the
// partition, is here too. The graphical wizard's install step calls the same
// function, so installing from either screen gives the same result.
//
// tui_linux.go - 글자 메뉴와 빌드 파이프라인.
//
// 화면이 있으면 wizard_linux.go 의 그래픽 마법사가 먼저 뜨고, 프레임버퍼가
// 없거나 열리지 않으면 이 메뉴로 온다. 시리얼 콘솔에서도 그대로 보이도록
// 한 열, 한 줄씩 흘러가는 텍스트만 쓴다. 화면 클리어는 VT100 escape 하나
// (\x1b[H\x1b[2J) 로 통일한다.
//
// 메뉴는 숫자를 눌러 선택하고 q 로 뒤로 간다. 자유 텍스트 입력은 프롬프트
// 뒤에 그대로 타이핑한다.
//
// 실제로 DSM 을 내려받아 패치하고 파티션에 쓰는 buildFlow 도 여기 있다.
// 그래픽 마법사의 설치 단계가 같은 함수를 부르므로, 어느 화면으로
// 설치하든 결과가 같다.
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"vibeldr/internal/catalog"
	"vibeldr/internal/cmdline"
	"vibeldr/internal/config"
	"vibeldr/internal/hwscan"
	"vibeldr/internal/image"
	"vibeldr/internal/initbin"
	"vibeldr/internal/kmod"
	"vibeldr/internal/kpatch"
	"vibeldr/internal/pat"
	"vibeldr/internal/ramdisk"
	"vibeldr/internal/ui"
)

// configPathOnLoader is where loader.yaml sits inside the mounted partition 1.
// configPathOnLoader - 마운트된 로더 파티션 1 안에서의 loader.yaml 위치.
const (
	loaderMount    = "/mnt/loader"
	configPath     = loaderMount + "/loader.yaml"
	workDir        = "/work"
	patchedRamdisk = "initrd-dsm"
	patchedZImage  = "zImage-dsm"
	logPath        = "/tmp/vibeldr.log"
)

// imageLabels are the volume labels the image builder puts on the three FAT
// partitions, the same as in `cmd/vibeldr/main.go`.
//
// imageLabels - 이미지 빌더가 세 FAT 파티션에 심는 볼륨 라벨.
// (`cmd/vibeldr/main.go` 의 것과 동일.)
var imageLabels = [3]string{"VIBELDR1", "VIBELDR2", "VIBELDR3"}

// sasCapablePlatformsTUI duplicates internal/config's sasCapablePlatforms,
// which is package private and cannot be taken as it is. Copying the list here
// keeps the import from going in a circle. If the list on the config side
// grows, this one has to be changed too.
//
// sasCapablePlatformsTUI - internal/config 의 sasCapablePlatforms 가 package
// private 이라 그대로 못 가져온다. 순환 import 없이 같은 목록을 여기에
// 복제한다. config 쪽이 늘어나면 이쪽도 같이 손대야 한다.
var sasCapablePlatformsTUI = map[string]bool{
	"purley": true, "broadwellnkv2": true, "epyc7002": true, "epyc7003": true,
	"epyc7003ntb": true, "geminilakenk": true, "icelaked": true,
	"r1000nk": true, "v1000nk": true,
}

// expansionChoices are the kinds of expansion unit the dtb synthesis knows. It
// is the same set as validExpansionKinds in internal/config, as a sorted list.
//
// expansionChoices - dtb 합성 코드가 인식하는 확장 유닛 종류.
// (internal/config 의 validExpansionKinds 와 동일한 집합, 정렬된 리스트.)
type expansionSpec struct {
	kind        string
	defaultBays int
}

var expansionChoices = []expansionSpec{
	{"DX513", 5}, {"DX517", 5}, {"DX1215", 12},
	{"RX415", 4}, {"RX1217", 12}, {"RX1217RP", 12},
}

// tuiState is the TUI's whole state.
//
// dirty marks that the user has changed the config and it has not reached
// loader.yaml yet. Where the partition mounted, an automatic save right after
// each edit makes it clean again; where the mount failed it stays dirty.
//
// tuiState - TUI 전역 상태.
//
// dirty 는 사용자가 config 를 건드렸는데 아직 loader.yaml 로 flush 되지
// 못한 상태를 표시한다. 파티션 마운트가 성공했으면 매 편집 직후에 자동
// 저장으로 다시 clean 이 되지만, 마운트가 실패했으면 계속 dirty 로 남는다.
type tuiState struct {
	cfg   *config.Config
	cat   *catalog.Catalog
	disk  LoaderDisk
	dirty bool
	// built says whether a build finished this boot and the payload reached
	// partition 3.
	//
	// built - 이번 부팅에서 빌드가 끝나 파티션 3 에 페이로드를 썼는지.
	built bool
	// packFromDisk says the driver pack was read off partition 4, in which
	// case the same content is not written back.
	//
	// packFromDisk - 드라이버 팩을 파티션 4 에서 읽어왔는지. 그렇다면
	// 같은 내용을 다시 쓰지 않는다.
	packFromDisk bool
	// next is the item chosen when Enter is pressed with nothing typed. It
	// moves on as each step finishes, so pressing Enter over and over walks
	// from the settings through to the build in order.
	//
	// next - 아무것도 입력하지 않고 엔터만 쳤을 때 고를 항목.
	// 한 단계를 끝내면 그 다음 단계로 옮겨 가므로, 엔터만 연달아 쳐도
	// 설정에서 빌드까지 순서대로 진행된다.
	next string
	// kexec is filled in once the build finishes: what a jump straight into
	// the DSM kernel just built needs. It skips one reboot.
	//
	// kexec - 빌드가 끝나면 채워지는, 방금 만든 DSM 커널로 바로 점프하기
	// 위한 재료. 재부팅을 한 번 건너뛴다.
	kexec kexecPlan
}

// kexecPlan is what a kexec jump into the installed DSM kernel needs.
// kexecPlan - 설치한 DSM 커널로 kexec 점프할 재료.
type kexecPlan struct {
	// kernel and initrd are the paths of the patched kernel and ramdisk in the
	// work directory. They are the same files that went onto partition 3, so
	// nothing has to be mounted again.
	//
	// kernel / initrd - 작업 디렉터리에 있는 패치된 커널과 램디스크 경로.
	// 파티션 3 에 쓴 것과 같은 파일이라 다시 마운트할 필요가 없다.
	kernel  string
	initrd  string
	cmdline string
	// ready says whether the three values above are filled in.
	// ready - 위 세 값이 채워졌는지.
	ready bool
}

// nextStep is what to do next given the settings as they stand.
//
// It is worked out afresh from the state every time, so that the cursor does
// not end up pointing somewhere odd when the user edits an item in the middle.
//
// nextStep - 지금 설정 상태에서 이어서 할 일.
//
// 매번 상태를 보고 다시 계산한다. 사용자가 중간 항목을 직접 고쳐도 커서가
// 엉뚱한 곳을 가리키지 않게 하기 위함이다.
func (st *tuiState) nextStep() string {
	switch {
	case st.cfg.Model == "":
		return "1"
	case st.cfg.DSM.Version == "" || st.cfg.DSM.URL == "":
		return "2"
	case st.built:
		return "6"
	default:
		return "5"
	}
}

// runTUI is the boot environment's main loop. This is PID 1, so returning
// normally is a kernel panic; the only return is after a reboot syscall.
//
// runTUI - 부팅 환경의 메인 루프. PID 1 이므로 정상 반환하는 순간
// 커널 패닉이다. 반환은 오직 재부팅 syscall 뒤에서만 일어난다.
func runTUI() {
	cat, err := catalog.Load()
	if err != nil {
		screenClear()
		fmt.Println("catalog load failed:", err)
		rescueLoop()
		return
	}

	disk, err := findAndCreateLoaderDisk(15e9) // 15 s
	if err != nil {
		screenClear()
		fmt.Println("loader disk not found:", err)
		fmt.Println("boot menu unavailable; entering rescue prompt")
		rescueLoop()
		return
	}
	if err := mountLoaderConfig(disk, loaderMount); err != nil {
		screenClear()
		fmt.Println("mount", loaderMount, "failed:", err)
		fmt.Println("config changes will not persist; entering menu anyway")
	}

	st := &tuiState{
		cfg:  loadConfigOrDefault(cat),
		cat:  cat,
		disk: disk,
	}

	// With a screen, put the graphical wizard up. A wizard that runs to the
	// end reboots from inside itself and never comes back here. With no
	// screen, or when the user closes the wizard, the text menu below follows.
	//
	// 화면이 있으면 그래픽 마법사를 띄운다. 마법사가 끝까지 가면 그 안에서
	// 재부팅하므로 여기로 돌아오지 않는다. 화면이 없거나 사용자가 마법사를
	// 닫으면 아래 글자 메뉴로 이어진다.
	if runGUIWizard(st) {
		if st.dirty {
			if err := saveConfig(st.cfg); err == nil {
				st.dirty = false
			}
		}
	}

	for {
		st.next = st.nextStep()
		choice := mainMenu(st)
		if choice == "" {
			// Enter alone; taken as choosing whatever comes next.
			// 엔터만 친 경우. 지금 이어서 할 일을 고른 것으로 본다.
			choice = st.next
		}
		switch choice {
		case "1":
			if modelMenu(st) {
				st.dirty = true
			}
		case "2":
			if dsmMenu(st) {
				st.dirty = true
			}
		case "3":
			if identityMenu(st) {
				st.dirty = true
			}
		case "4":
			if storageMenu(st) {
				st.dirty = true
			}
		case "5":
			buildFlow(st, textBuildIO{})
		case "6":
			bootIntoDSM(st)
		case "R", "r":
			rescueMode()
		case "9":
			rescueLoop()
		case "?":
			helpScreen()
		default:
			continue
		}
		// Save automatically after every screen, failing quietly when the
		// partition is not mounted. The dirty flag only clears on a save that
		// actually worked.
		//
		// 매 화면 후 자동 저장. 파티션이 안 붙었어도 조용히 실패한다.
		// 저장이 성공한 경우에만 dirty flag 를 지운다.
		if st.dirty {
			if err := saveConfig(st.cfg); err == nil {
				st.dirty = false
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Screens / 화면
// ---------------------------------------------------------------------------

func mainMenu(st *tuiState) string {
	screenClear()
	title := "vibeldr boot menu"
	if st.dirty {
		title += "   [unsaved]"
	}
	fmt.Println(title)
	fmt.Println("-----------------")
	fmt.Printf("  model      : %s\n", st.cfg.Model)
	fmt.Printf("  dsm version: %s\n", st.cfg.DSM.Version)
	fmt.Printf("  serial     : %s\n", ifEmpty(st.cfg.Identity.Serial, "<auto>"))
	fmt.Printf("  nics       : %d\n", st.cfg.Identity.NICCount)
	fmt.Println()
	// Mark whatever comes next and make plain Enter choose it, so that someone
	// using this for the first time does not have to work out what to pick.
	//
	// 지금 이어서 할 일에 표시를 달고, 그냥 엔터를 치면 그게 선택되게 한다.
	// 처음 쓰는 사람이 무엇을 먼저 골라야 하는지 고민하지 않아도 된다.
	item := func(key, label string) {
		mark := "  "
		if key == st.next {
			mark = "▶ "
		}
		fmt.Printf("%s%s) %s\n", mark, key, label)
	}
	item("1", "Model")
	item("2", "DSM version + download URL")
	item("3", "Identity (serial / MACs)")
	item("4", "Storage overrides")
	item("5", "Fetch and build (writes new payload to partition 3)")
	item("6", "Reboot into DSM")
	fmt.Println("  R) 복구 모드 (Rescue Mode - BTRFS 진단/복구)")
	fmt.Println("  9) Rescue shell")
	fmt.Println()
	fmt.Println("  ?) help")
	return prompt(fmt.Sprintf("select [%s]: ", st.next))
}

// helpScreen is a one-screen explanation of what each menu does, so that
// someone who booted over a serial console knows which item to pick without
// going to find a manual.
//
// helpScreen - 각 메뉴가 무엇을 하는지 한 화면짜리 설명. 시리얼 콘솔로
// 부팅한 사용자가 매뉴얼을 찾지 않고도 어느 항목을 골라야 할지 알도록.
func helpScreen() {
	screenClear()
	fmt.Println("vibeldr boot menu - help")
	fmt.Println("------------------------")
	fmt.Println()
	fmt.Println("  1) Model")
	fmt.Println("     흉내낼 시놀로지 모델을 고른다 (예: SA6400, DS918+).")
	fmt.Println("     모델을 바꾸면 지원 DSM 버전 목록도 함께 바뀐다.")
	fmt.Println()
	fmt.Println("  2) DSM version + download URL")
	fmt.Println("     이 모델의 알려진 릴리스 목록에서 하나를 고른다. 고르는")
	fmt.Println("     즉시 다운로드 URL 과 기대 MD5 가 함께 채워진다. 목록이")
	fmt.Println("     비어있는 모델은 X.Y.Z-N 형식으로 직접 타이핑.")
	fmt.Println()
	fmt.Println("  3) Identity (serial / MACs)")
	fmt.Println("     DSM 이 이 머신을 인식하는 시리얼과 MAC 을 정한다.")
	fmt.Println("     비워두면 빌드 시점에 자동 생성된 뒤 여기에 고정된다.")
	fmt.Println()
	fmt.Println("  4) Storage overrides")
	fmt.Println("     베이 수 / NVMe 슬롯 수 / 확장 유닛 / SAS / 포트맵.")
	fmt.Println("     대부분 모델 기본값 그대로가 정답 - 필요할 때만 손댄다.")
	fmt.Println()
	fmt.Println("  5) Fetch and build")
	fmt.Println("     .pat 다운로드 -> 커널/램디스크 패치 -> 파티션 3 덮어쓰기.")
	fmt.Println("     끝난 뒤 6) 로 재부팅하면 새 페이로드로 DSM 이 뜬다.")
	fmt.Println()
	fmt.Println("  R) 복구 모드 (Rescue Mode)")
	fmt.Println("     BTRFS 볼륨 스캔 / 읽기전용 마운트 / check + rescue /")
	fmt.Println("     scrub status / subvolume 나열 / snapshot 롤백.")
	fmt.Println("     되돌릴 수 없는 항목은 'YES' 를 정확히 입력해야 진행.")
	fmt.Println()
	fmt.Println("  9) Rescue shell")
	fmt.Println("     ls / cat / ip / dmesg / parts / logs / mount / lsblk /")
	fmt.Println("     net / journal / btrfs / shell / reboot.")
	fmt.Println()
	fmt.Printf("  Log for this session: %s\n", logPath)
	fmt.Println("  (rescue 프롬프트에서 `logs " + logPath + "` 로 볼 수 있음)")
	fmt.Println()
	pause("")
}

// modelMenu picks one of the verified models and sets it.
//
// It shows catalog.CuratedModels rather than the whole catalogue. Those three
// are the only combinations actually verified, and showing any other model
// would leave the user digging with no idea what does and does not work. It
// returns true if anything was edited.
//
// modelMenu - 검증된 대표 모델을 골라 세팅한다.
//
// 카탈로그 전체가 아니라 catalog.CuratedModels 만 보여준다. 우리가 실제로
// 검증한 조합이 그 세 개뿐이라, 그 밖의 모델을 노출하면 사용자가 무엇이
// 되고 안 되는지 모른 채로 삽질하게 된다. 편집이 일어났으면 true 를 돌려준다.
func modelMenu(st *tuiState) bool {
	models := catalog.CuratedModels
	for {
		screenClear()
		fmt.Println("Models")
		fmt.Println("------")
		for i, m := range models {
			marker := " "
			if strings.EqualFold(m, st.cfg.Model) {
				marker = "*"
			}
			plat, _ := st.cat.PlatformForModel(m)
			pname := "?"
			if plat != nil {
				pname = plat.Name
			}
			fmt.Printf(" %s %d) %-14s  (%s)\n", marker, i+1, m, pname)
		}
		fmt.Println()
		fmt.Println("  q) back")
		in := prompt("enter number or command: ")
		switch in {
		case "q", "":
			return false
		default:
			n, err := strconv.Atoi(in)
			if err != nil || n < 1 || n > len(models) {
				continue
			}
			pick := models[n-1]
			if pick == st.cfg.Model {
				return false
			}
			plat, _ := st.cat.PlatformForModel(pick)
			st.cfg.Model = pick
			// If the new model does not support the current DSM version,
			// rebase onto that platform's newest productver.
			//
			// 새 모델이 현재 DSM 버전을 지원 안 하면 그 플랫폼의
			// 최신 productver 로 rebase 한다.
			if plat != nil {
				if _, ok := plat.Kernels[st.cfg.ProductVersion()]; !ok {
					vers := st.cat.ProductVersions(plat.Name)
					if len(vers) > 0 {
						st.cfg.DSM.Version = vers[0] + ".0-00000"
					}
				}
			}
			// After a model change the old identity most likely no longer
			// fits the rules, so the user is told but nothing is deleted
			// automatically. Not deleting is the conservative choice; the
			// identity menu can generate a new one.
			//
			// 모델이 바뀌면 기존 identity 는 규칙이 안 맞을 확률이 크므로
			// 사용자에게 알림만 남기고 자동으론 지우지 않는다. 안 지우는
			// 쪽이 보수적이고, 사용자가 identity 메뉴에서 다시 만들면 된다.
			pause("model set to " + st.cfg.Model + " (regenerate identity if it no longer validates)")
			return true
		}
	}
}

// dsmMenu sets the URL and MD5 along with the choice when the catalogue knows a
// release for this model. For a model the catalogue does not know, (t) takes
// them typed in.
//
// dsmMenu - 카탈로그가 아는 릴리스가 있으면 번호 선택으로 URL/MD5 까지
// 함께 세팅한다. 카탈로그에 없는 모델은 (t) 로 직접 타이핑한다.
func dsmMenu(st *tuiState) bool {
	changed := false
	for {
		screenClear()
		fmt.Println("DSM version")
		fmt.Println("-----------")
		plat, _ := st.cat.PlatformForModel(st.cfg.Model)
		if plat == nil {
			pause("model has no platform in the catalog")
			return changed
		}
		fmt.Printf("  current: %s\n", st.cfg.DSM.Version)
		fmt.Printf("  url    : %s\n", ifEmpty(st.cfg.DSM.URL, "<not set>"))
		fmt.Printf("  md5    : %s\n", ifEmpty(st.cfg.DSM.MD5, "<not set>"))
		fmt.Println()

		known := st.cat.KnownVersions(st.cfg.Model)
		if len(known) > 0 {
			fmt.Printf("Known releases for %s (newest first):\n", st.cfg.Model)
			for i, v := range known {
				marker := " "
				if v == st.cfg.DSM.Version {
					marker = "*"
				}
				rel, _ := st.cat.ReleaseFor(st.cfg.Model, v)
				fmt.Printf(" %s %2d) %s\n", marker, i+1, v)
				if rel.URL != "" {
					fmt.Printf("        url: %s\n", rel.URL)
				}
				if rel.MD5 != "" {
					fmt.Printf("        md5: %s\n", rel.MD5)
				}
			}
			fmt.Println()
		} else {
			// A model the catalogue does not know; only the productver hint
			// is shown.
			//
			// 카탈로그에 없는 모델. productver 힌트만 보여준다.
			vers := st.cat.ProductVersions(plat.Name)
			fmt.Printf("No release entries for %s.\n", st.cfg.Model)
			fmt.Printf("Supported product versions on platform %s:\n", plat.Name)
			for _, v := range vers {
				fmt.Printf("  - %s   kernel %s\n", v, plat.Kernels[v])
			}
			fmt.Println("  (use `t` to type the full X.Y.Z-N and `u`/`m` for URL/MD5.)")
			fmt.Println()
		}

		fmt.Println("  t) type full X.Y.Z-N")
		fmt.Println("  u) set download URL")
		fmt.Println("  m) set expected MD5")
		fmt.Println("  q) back")
		in := prompt("select: ")
		switch in {
		case "t":
			v := prompt("full DSM version (e.g. 7.4.1-90080): ")
			if v != "" {
				v = strings.TrimSpace(v)
				if v != st.cfg.DSM.Version {
					st.cfg.DSM.Version = v
					changed = true
				}
			}
		case "u":
			v := strings.TrimSpace(prompt("URL: "))
			if v != st.cfg.DSM.URL {
				st.cfg.DSM.URL = v
				changed = true
			}
		case "m":
			v := strings.TrimSpace(prompt("MD5 (32 hex, empty to disable check): "))
			if v != st.cfg.DSM.MD5 {
				st.cfg.DSM.MD5 = v
				changed = true
			}
		case "q", "":
			return changed
		default:
			if len(known) == 0 {
				continue
			}
			n, err := strconv.Atoi(in)
			if err != nil || n < 1 || n > len(known) {
				continue
			}
			pick := known[n-1]
			rel, _ := st.cat.ReleaseFor(st.cfg.Model, pick)
			// Set what the catalogue knows as it is. An empty URL or MD5
			// means that entry has not been filled in yet and the user has
			// to supply it by hand.
			//
			// 카탈로그가 아는 값을 그대로 세팅한다. URL / MD5 가 비어 있으면
			// 아직 채워지지 않은 항목이라 사용자가 손으로 채워야 한다.
			st.cfg.DSM.Version = pick
			st.cfg.DSM.URL = rel.URL
			st.cfg.DSM.MD5 = rel.MD5
			changed = true
			pause("set to " + pick)
		}
	}
}

// identityMenu edits the serial number and MAC addresses, returning true if
// anything was edited.
//
// identityMenu - 시리얼 번호와 MAC 주소를 편집한다. 편집이 일어났으면
// true 를 돌려준다.
func identityMenu(st *tuiState) bool {
	changed := false
	for {
		screenClear()
		fmt.Println("Identity")
		fmt.Println("--------")
		fmt.Printf("  serial : %s\n", ifEmpty(st.cfg.Identity.Serial, "<auto - will generate at build time>"))
		fmt.Printf("  macs   : %d entries\n", len(st.cfg.Identity.MACs))
		for i, m := range st.cfg.Identity.MACs {
			fmt.Printf("           %d: %s\n", i+1, m)
		}
		fmt.Printf("  nics   : %d\n", st.cfg.Identity.NICCount)
		fmt.Printf("  vid/pid: %s / %s\n", st.cfg.Identity.VID, st.cfg.Identity.PID)
		fmt.Println()
		fmt.Println("  r) regenerate serial + MACs (overwrites the pinned values)")
		fmt.Println("  k) keep - just view")
		fmt.Println("  n) set NIC count")
		fmt.Println("  s) type serial manually")
		fmt.Println("  c) clear (revert to <auto>)")
		fmt.Println("  q) back")
		in := prompt("select: ")
		switch in {
		case "r":
			if _, ok := st.cat.SerialRule(st.cfg.Model); !ok {
				pause("no serial rule for " + st.cfg.Model + " - not regenerating")
				continue
			}
			s, err := st.cat.GenerateSerial(st.cfg.Model)
			if err != nil {
				pause("serial: " + err.Error())
				continue
			}
			macs, err := st.cat.GenerateMACs(st.cfg.Model, st.cfg.Identity.NICCount)
			if err != nil {
				pause("macs: " + err.Error())
				continue
			}
			st.cfg.Identity.Serial = s
			st.cfg.Identity.MACs = macs
			changed = true
			pause("regenerated (remember to run Fetch & Build so the boot menu picks it up)")
		case "k", "q", "":
			return changed
		case "n":
			v := prompt("NIC count (1..8): ")
			n, err := strconv.Atoi(v)
			if err == nil && n >= 1 && n <= 8 && n != st.cfg.Identity.NICCount {
				st.cfg.Identity.NICCount = n
				changed = true
			}
		case "s":
			v := strings.ToUpper(strings.TrimSpace(prompt("serial: ")))
			if v == "" {
				continue
			}
			if err := st.cat.ValidateSerial(st.cfg.Model, v); err != nil {
				pause("rejected: " + err.Error())
				continue
			}
			if v != st.cfg.Identity.Serial {
				st.cfg.Identity.Serial = v
				changed = true
			}
		case "c":
			if st.cfg.Identity.Serial != "" || len(st.cfg.Identity.MACs) > 0 {
				st.cfg.Identity.Serial = ""
				st.cfg.Identity.MACs = nil
				changed = true
			}
		}
	}
}

// storageMenu is the editor for the static storage overrides.
//
// It edits the SATA portmap, disk_idx_map, sata_remap, usb_as_internal and ncq,
// plus bays, nvme_slots, expansion units and sas for the dtb synthesis. The SAS
// toggle only appears where the platform drives mpt3sas.
//
// storageMenu - 정적 스토리지 의도 override 편집기.
//
// 편집 대상: SATA portmap / disk_idx_map / sata_remap / usb_as_internal / ncq
// 그리고 dtb 합성용의 bays / nvme_slots / expansion units / sas.
// SAS 토글은 플랫폼이 mpt3sas 를 도리는 경우에만 노출한다.
func storageMenu(st *tuiState) bool {
	changed := false
	plat, _ := st.cat.PlatformForModel(st.cfg.Model)
	platName := ""
	if plat != nil {
		platName = plat.Name
	}
	sasVisible := platName != "" && sasCapablePlatformsTUI[platName]

	for {
		screenClear()
		fmt.Println("Storage")
		fmt.Println("-------")
		if platName != "" {
			fmt.Printf("  (platform: %s)\n", platName)
		}
		fmt.Printf("  bays            : %s\n", intOrDefault(st.cfg.Storage.Bays))
		fmt.Printf("  nvme_slots      : %s\n", intOrDefault(st.cfg.Storage.NVMeSlots))
		fmt.Printf("  expansion       : %s\n", formatExpansion(st.cfg.Storage.Expansion))
		if sasVisible {
			fmt.Printf("  sas             : %v\n", st.cfg.Storage.SAS)
		}
		fmt.Printf("  sata_portmap    : %s\n", ifEmpty(st.cfg.Storage.SataPortMap, "<unset>"))
		fmt.Printf("  disk_idx_map    : %s\n", ifEmpty(st.cfg.Storage.DiskIdxMap, "<unset>"))
		fmt.Printf("  sata_remap      : %s\n", ifEmpty(st.cfg.Storage.SataRemap, "<unset>"))
		fmt.Printf("  usb_as_internal : %v\n", st.cfg.Storage.USBAsInternal)
		fmt.Printf("  ncq             : %v\n", st.cfg.Storage.NCQ)
		fmt.Println()
		fmt.Println("  1) set bays (0 = platform default)")
		fmt.Println("  2) set nvme_slots (0 = platform default)")
		fmt.Println("  3) edit expansion units")
		if sasVisible {
			fmt.Println("  4) toggle sas")
		} else {
			fmt.Println("  4) (sas not supported on this platform)")
		}
		fmt.Println("  5) toggle usb_as_internal")
		fmt.Println("  6) toggle ncq")
		fmt.Println("  7) set sata_portmap")
		fmt.Println("  8) set disk_idx_map")
		fmt.Println("  9) set sata_remap")
		fmt.Println("  q) back")
		in := prompt("select: ")
		switch in {
		case "1":
			v := prompt("bays (0..64, 0 = platform default): ")
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err == nil && n >= 0 && n <= 64 && n != st.cfg.Storage.Bays {
				st.cfg.Storage.Bays = n
				changed = true
			}
		case "2":
			v := prompt("nvme_slots (0..8, 0 = platform default): ")
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err == nil && n >= 0 && n <= 8 && n != st.cfg.Storage.NVMeSlots {
				st.cfg.Storage.NVMeSlots = n
				changed = true
			}
		case "3":
			if editExpansion(st) {
				changed = true
			}
		case "4":
			if sasVisible {
				st.cfg.Storage.SAS = !st.cfg.Storage.SAS
				changed = true
			}
		case "5":
			st.cfg.Storage.USBAsInternal = !st.cfg.Storage.USBAsInternal
			changed = true
		case "6":
			st.cfg.Storage.NCQ = !st.cfg.Storage.NCQ
			changed = true
		case "7":
			v := strings.TrimSpace(prompt("sata_portmap (empty to clear): "))
			if v != st.cfg.Storage.SataPortMap {
				st.cfg.Storage.SataPortMap = v
				changed = true
			}
		case "8":
			v := strings.TrimSpace(prompt("disk_idx_map (empty to clear): "))
			if v != st.cfg.Storage.DiskIdxMap {
				st.cfg.Storage.DiskIdxMap = v
				changed = true
			}
		case "9":
			v := strings.TrimSpace(prompt("sata_remap (empty to clear): "))
			if v != st.cfg.Storage.SataRemap {
				st.cfg.Storage.SataRemap = v
				changed = true
			}
		case "q", "":
			return changed
		}
	}
}

// editExpansion is the editor for the list of expansion units.
// editExpansion - 확장 유닛 리스트 편집기.
func editExpansion(st *tuiState) bool {
	changed := false
	for {
		screenClear()
		fmt.Println("Expansion units")
		fmt.Println("---------------")
		if len(st.cfg.Storage.Expansion) == 0 {
			fmt.Println("  (none)")
		}
		for i, e := range st.cfg.Storage.Expansion {
			fmt.Printf("  %d) %s (%d bays)\n", i+1, e.Kind, e.Bays)
		}
		fmt.Println()
		fmt.Println("  a) add a unit")
		fmt.Println("  d) remove the last unit")
		fmt.Println("  c) clear all")
		fmt.Println("  q) back")
		in := prompt("select: ")
		switch in {
		case "a":
			screenClear()
			fmt.Println("Pick expansion kind")
			fmt.Println("-------------------")
			fmt.Println("  0) none (cancel)")
			for i, s := range expansionChoices {
				fmt.Printf("  %d) %s (default %d bays)\n", i+1, s.kind, s.defaultBays)
			}
			pick := strings.TrimSpace(prompt("select: "))
			n, err := strconv.Atoi(pick)
			if err != nil || n < 0 || n > len(expansionChoices) {
				continue
			}
			if n == 0 {
				continue
			}
			spec := expansionChoices[n-1]
			bays := spec.defaultBays
			if v := strings.TrimSpace(prompt(fmt.Sprintf("bays for %s [%d]: ", spec.kind, bays))); v != "" {
				if b, err := strconv.Atoi(v); err == nil && b >= 1 && b <= 12 {
					bays = b
				}
			}
			st.cfg.Storage.Expansion = append(st.cfg.Storage.Expansion,
				config.ExpansionUnit{Kind: spec.kind, Bays: bays})
			changed = true
		case "d":
			if n := len(st.cfg.Storage.Expansion); n > 0 {
				st.cfg.Storage.Expansion = st.cfg.Storage.Expansion[:n-1]
				changed = true
			}
		case "c":
			if len(st.cfg.Storage.Expansion) > 0 {
				st.cfg.Storage.Expansion = nil
				changed = true
			}
		case "q", "":
			return changed
		}
	}
}

// intOrDefault shows 0 as "<default>" and anything else as a plain decimal.
// intOrDefault - 0 을 "<default>" 로 표시한다. 나머지는 그대로 십진수.
func intOrDefault(n int) string {
	if n == 0 {
		return "<default>"
	}
	return strconv.Itoa(n)
}

func formatExpansion(units []config.ExpansionUnit) string {
	if len(units) == 0 {
		return "<none>"
	}
	parts := make([]string, len(units))
	for i, u := range units {
		parts[i] = fmt.Sprintf("%s(%d)", u.Kind, u.Bays)
	}
	return strings.Join(parts, ", ")
}

// collectExtractedDSM reads what scemd already unpacked into dsmDir and puts it
// back together as a pat.ExtractResult, which the rest of the pipeline takes
// from there. Without zImage and rd.gz it is an error.
//
// collectExtractedDSM - scemd 가 dsmDir 안에 이미 풀어 놓은 파일들을 읽어
// pat.ExtractResult 형태로 재구성한다. 파이프라인 뒷단이 이걸 그대로
// 이어받는다. zImage/rd.gz 이 없으면 에러다.
func collectExtractedDSM(dsmDir string) (*pat.ExtractResult, error) {
	res := &pat.ExtractResult{
		Format: pat.FormatEncrypted,
		Files:  make(map[string]string),
		Hashes: make(map[string]string),
	}
	for _, name := range pat.WantedFiles {
		p := filepath.Join(dsmDir, name)
		f, err := os.Open(p)
		if err != nil {
			res.Missing = append(res.Missing, name)
			continue
		}
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			return nil, fmt.Errorf("hash %s: %w", name, err)
		}
		f.Close()
		res.Files[name] = p
		res.Hashes[name] = hex.EncodeToString(h.Sum(nil))
	}
	for _, essential := range []string{"zImage", "rd.gz"} {
		if _, ok := res.Files[essential]; !ok {
			return nil, fmt.Errorf("scemd 결과에 %s 없음 (dir=%s)", essential, dsmDir)
		}
	}
	return res, nil
}

// zImageKernelVersion is the kernel version a bzImage wrote into its own header.
//
// At 0x20e in the setup header there is a u16, and at that value plus 0x200
// sits a string like "4.4.302+ (root@build7) ...". The version runs up to the
// first space. Unreadable comes back empty, which means skip the check rather
// than fail.
//
// zImageKernelVersion - bzImage 가 자기 헤더에 적어 둔 커널 버전.
//
// setup 헤더의 0x20e 에 u16 이 있고, 그 값에 0x200 을 더한 자리에
// "4.4.302+ (root@build7) ..." 같은 문자열이 있다. 첫 공백까지가 버전이다.
// 읽을 수 없으면 빈 문자열 - 확인을 건너뛰라는 뜻이지 실패가 아니다.
func zImageKernelVersion(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	var hdr [2]byte
	if _, err := f.ReadAt(hdr[:], 0x20e); err != nil {
		return ""
	}
	buf := make([]byte, 96)
	n, _ := f.ReadAt(buf, int64(binary.LittleEndian.Uint16(hdr[:]))+0x200)
	if n <= 0 {
		return ""
	}
	s := string(buf[:n])
	if i := strings.IndexAny(s, " \x00\n"); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

// diskLayoutMB reads the loader disk's real partition sizes in MiB.
//
// An install builds a fresh loader image internally and then writes that
// image's partition slice whole onto the same-numbered partition of the real
// disk. If the two layouts disagree it does not fit:
//
//	flash p3: payload 670040064 bytes does not fit in /dev/synoboot3 (133169152 bytes)
//
// Hard-coding the layout would break for certain the moment an image is burned
// at a different size, so it is read from the disk that is booted right now.
// Unreadable comes back as 0 and the caller falls back to the old defaults.
//
// diskLayoutMB - 로더 디스크의 실제 파티션 크기를 MiB 로 읽는다.
//
// 설치는 내부에서 로더 이미지를 새로 만든 뒤 그 파티션 조각을 실제 디스크의
// 같은 번호 파티션에 통째로 쓴다. 그래서 두 배치가 어긋나면 위 영문의 오류처럼
// 들어가지 않는다. 배치를 코드에 박아두면 이미지를 다른 크기로 구웠을 때
// 반드시 깨지므로, 지금 부팅한 디스크에서 직접 읽는다. 못 읽으면 0 을 돌려주고
// 호출부가 기존 기본값으로 돌아간다.
func diskLayoutMB(d LoaderDisk) (total, p1, p2, p4 int) {
	name := d.Kernel
	if name == "" {
		return 0, 0, 0, 0
	}
	// A name ending in a digit, such as nvme0n1, takes "p1" for its
	// partition; otherwise just "1".
	//
	// nvme0n1 처럼 숫자로 끝나는 이름은 파티션이 "p1", 아니면 "1".
	sep := ""
	if n := len(name); n > 0 && name[n-1] >= '0' && name[n-1] <= '9' {
		sep = "p"
	}
	sectors := func(suffix string) int64 {
		b, err := os.ReadFile(filepath.Join("/sys/block", name, name+suffix, "size"))
		if err != nil {
			return 0
		}
		v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		if err != nil {
			return 0
		}
		return v
	}
	whole := func() int64 {
		b, err := os.ReadFile(filepath.Join("/sys/block", name, "size"))
		if err != nil {
			return 0
		}
		v, _ := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
		return v
	}
	const perMB = (1 << 20) / 512
	total = int(whole() / perMB)
	p1 = int(sectors(sep+"1") / perMB)
	p2 = int(sectors(sep+"2") / perMB)
	p4 = int(sectors(sep+"4") / perMB)
	return total, p1, p2, p4
}

// buildFlow is the whole pipeline: fetch, extract, patch, image, overwrite
// partition 3.
//
// buildFlow - fetch -> extract -> patch -> image -> partition 3 덮어쓰기.
func buildFlow(st *tuiState, bio buildIO) {
	cfg := st.cfg
	cat := st.cat
	disk := st.disk

	screenClear()
	fmt.Println("Fetch and build")
	fmt.Println("---------------")

	// Validate first, so that a bad setting does not waste a few hundred MB of
	// download before failing.
	//
	// 검증부터. 잘못된 설정으로 실행해서 몇 백 MB 다운받고 실패하는 낭비를
	// 막는다.
	if err := cfg.Validate(cat); err != nil {
		fmt.Println("configuration is not valid:")
		fmt.Println(" ", err)
		bio.Pause("")
		return
	}

	// With no URL, take one here and now. A release the catalogue knows was
	// already filled in by dsmMenu.
	//
	// URL 이 없으면 여기서 즉석 입력을 받는다. 카탈로그에 있는 릴리스는
	// dsmMenu 에서 이미 채워졌을 것이다.
	if strings.TrimSpace(cfg.DSM.URL) == "" {
		v := strings.TrimSpace(bio.Prompt("dsm.url (Synology download URL for the .pat): "))
		if v == "" {
			bio.Pause("no URL - aborted")
			return
		}
		cfg.DSM.URL = v
		st.dirty = true
	}

	// Flush to loader.yaml before the build starts, so that a reboot in the
	// middle of a fetch resumes on the next boot with the same intent.
	//
	// 빌드 시작 전에 loader.yaml 로 flush 한다. 받는 도중 재부팅이 나더라도
	// 다음 부팅이 같은 의도로 재개되게 하려는 것이다.
	if err := saveConfig(cfg); err != nil {
		ui.Warn("could not persist loader.yaml: %v (continuing)", err)
	} else {
		st.dirty = false
	}

	// The work directory goes in RAM. The loader partition is for booting and
	// is no place for large output.
	//
	// 작업 디렉터리는 램에 둔다. 로더 파티션은 boot 전용이고 큰 산출물이
	// 들어갈 자리가 아니다.
	cfg.Paths.Work = workDir
	cfg.Paths.Cache = filepath.Join(workDir, "cache")
	cfg.Paths.Output = filepath.Join(workDir, "out")
	_ = os.MkdirAll(cfg.Paths.Cache, 0o755)
	_ = os.MkdirAll(cfg.Paths.Output, 0o755)

	plat, _ := cat.PlatformForModel(cfg.Model)

	id, err := resolveIdentityForBuild(cfg, cat)
	if err != nil {
		fmt.Println("identity:", err)
		bio.Pause("")
		return
	}
	// If one was generated, pin it into the config so the next boot uses the
	// same identity.
	//
	// 생성됐다면 config 에 pin 해 둔다. 다음 부팅에도 같은 identity 를 쓴다.
	cfg.Identity.Serial = id.Serial
	cfg.Identity.MACs = id.MACs
	if err := saveConfig(cfg); err == nil {
		st.dirty = false
	}

	// Check which bus the loader is actually on and pass that along. Leaving
	// the default ("sata") would put synoboot_satadom on a virtio-scsi loader
	// too, and the DSM install would then fail mounting the boot partition -
	// see the comments in loaderbus_linux.go.
	//
	// 로더가 실제로 붙어 있는 버스를 확인해서 넘긴다. 기본값("sata")을 그대로
	// 쓰면 virtio-scsi 로더에도 synoboot_satadom 이 붙어 DSM 설치가 부트 파티션
	// 마운트에서 실패한다 (loaderbus_linux.go 주석 참고).
	opts := cmdline.DefaultOptions()
	opts.LoaderBus = loaderBus(st.disk.Kernel)
	// A non-DT platform (DS3622xs+, DS918+) decides its bays from
	// SataPortMap and DiskIdxMap, and those values are only right when they
	// are worked out from the real controller layout.
	//
	// 비-DT 플랫폼(DS3622xs+/DS918+)은 SataPortMap/DiskIdxMap 으로 베이를
	// 정하는데, 그 값은 실제 컨트롤러 구성에서 계산해야 맞는다.
	if cs, err := hwscan.ScanSysfs(); err == nil {
		opts.Controllers = hwscan.OrderForBays(cs)
	}
	// Which NIC vendors are in this machine. With more than one - an Intel
	// alongside a Realtek, say - the cards register in a different order from
	// boot to boot, so the card mac1 lands on can change. SortnetifWanted turns
	// sortnetif on from this, and the order stops moving.
	//
	// 이 기계에 어떤 랜카드 벤더가 있는지. 둘 이상이면 (인텔 + 리얼텍 처럼)
	// 부팅마다 카드 등록 순서가 달라져 mac1 이 붙는 카드가 바뀔 수 있다.
	// SortnetifWanted 가 이 값을 보고 sortnetif 를 켜면 순서가 고정된다.
	opts.NICProfile = hwscan.DetectNICProfileSysfs()
	b, err := cmdline.Build(cfg, plat, id, opts)
	if err != nil {
		fmt.Println("cmdline:", err)
		bio.Pause("")
		return
	}
	if problems := b.Validate(); len(problems) > 0 {
		for _, p := range problems {
			fmt.Printf("  cmdline %s: %s\n", p.Key, p.Message)
		}
		bio.Pause("cmdline invalid - aborted")
		return
	}

	patPath := filepath.Join(cfg.Paths.Cache, fmt.Sprintf("%s-%s.pat", cfg.Model, cfg.DSM.Version))
	dsmDir := filepath.Join(cfg.Paths.Work, "dsm")

	// 0) With a pre-decrypted kernel on the image, skip the download and the
	// decryption. The bootstrap put it on P2 as
	// "dsm/<model>/{zImage,rd.gz,VERSION}".
	//
	// 0) 이미지에 미리 복호화해 둔 커널이 있으면 다운로드·복호화를 건너뛴다.
	// 부트스트랩 P2 에 "dsm/<모델>/{zImage,rd.gz,VERSION}" 로 심겨 있다.
	embedded := useEmbeddedDSM(st, cfg.Model, dsmDir)
	if embedded {
		ui.Section("Embedded DSM")
		ui.OK("이미지에 포함된 %s 커널 사용 - 다운로드 생략", cfg.Model)
	}

	if !embedded {

		// 1) fetch
		// 1) 내려받기
		ui.Section("Fetch")
		ui.Field("URL", cfg.DSM.URL)
		ui.Field("Destination", patPath)
		fmt.Println()
		fetcher := pat.NewFetcher()
		fetcher.OnProgress = func(done, total int64) { ui.ProgressBar("downloading", done, total) }
		if err := fetcher.Fetch(context.Background(), cfg.DSM.URL, patPath, cfg.DSM.MD5); err != nil {
			ui.ClearLine()
			fmt.Println("fetch failed:", err)
			bio.Pause("")
			return
		}
		ui.ClearLine()
		ui.OK("downloaded")

		// 2) extract
		// 2) 추출
		ui.Section("Extract")

		// Decide the format from the header first. A plain tar or tar.gz goes
		// straight to pat.Extract. An encrypted one - whichever variant,
		// Salted__, __PATFILE__, 35 AD BE EF and so on - is opened with
		// Synology's own extractor scemd, pulled out of a seed DSM and cached.
		// On a first boot that means downloading the seed DSM, which takes
		// some minutes.
		//
		// 헤더로 포맷 먼저 판정한다. 평문 tar / tar.gz 이면 그대로
		// pat.Extract. 암호화된 경우 (Salted__, __PATFILE__, 35 AD BE EF 등
		// 어떤 변종이든) 시놀로지 자체 추출기 scemd 를 씨앗 DSM 에서 뽑아
		// 캐시한 뒤 그걸로 푼다. 첫 부팅이면 씨앗 DSM 다운로드가 발생한다
		// (수 분).
		format, ferr := pat.DetectFormat(patPath)
		if ferr != nil {
			fmt.Println("format 검사 실패:", ferr)
			bio.Pause("")
			return
		}
		ui.Field("Format", format.String())
		if format == pat.FormatEncrypted {
			ui.Info("암호화된 pat 감지 - scemd 브릿지로 처리")
			scemdPath, serr := ensureScemd()
			if serr != nil {
				fmt.Println("scemd 준비 실패:", serr)
				bio.Pause("")
				return
			}
			ui.OK("scemd: %s", scemdPath)
			if derr := decryptPatWithScemd(scemdPath, patPath, dsmDir); derr != nil {
				fmt.Println("scemd 복호화 실패:", derr)
				bio.Pause("")
				return
			}
			ui.OK("복호화 완료 - %s", dsmDir)
		}

		res, err := pat.Extract(patPath, dsmDir)
		if err != nil {
			// On the encrypted path, parsing the original .pat again still
			// fails. scemd already unpacked zImage and rd.gz into dsmDir
			// above, so that result is reassembled instead and the pipeline
			// carries on.
			//
			// 암호화된 경로에서는 원본 .pat 을 다시 파싱하려 하면 여전히
			// 실패한다. scemd 가 위에서 이미 dsmDir 에 zImage/rd.gz 를 풀어
			// 놨으므로 그 결과를 대신 재구성해 파이프라인을 이어 간다.
			if format == pat.FormatEncrypted && errors.Is(err, pat.ErrEncrypted) {
				res, err = collectExtractedDSM(dsmDir)
			}
			if err != nil {
				fmt.Println("extract failed:", err)
				bio.Pause("")
				return
			}
		}
		for _, name := range pat.WantedFiles {
			if _, ok := res.Files[name]; ok {
				ui.OK("%s", name)
			} else {
				ui.Warn("%s missing", name)
			}
		}

	} // if !embedded / embedded 가 아닐 때의 끝

	// The kernel that was fetched has to belong to the model that was picked.
	//
	// A mismatch here does not announce itself: the install runs to the end,
	// DSM comes up, and only then does it turn out that the ramdisk belongs to
	// another platform, so our helper never runs and no network card is found.
	// Stopping now is the difference between one clear message and a long hunt.
	//
	// 받아 온 커널이 고른 모델의 것인지 확인한다.
	//
	// 어긋나도 티가 나지 않는다. 설치는 끝까지 "성공" 하고 DSM 도 뜨지만,
	// 램디스크가 다른 플랫폼 것이라 우리 헬퍼가 안 돌고 랜카드가 안 잡힌다.
	// 여기서 끊는 것과 안 끊는 것의 차이는 명확한 메시지 한 줄과 긴 추적이다.
	if want := plat.Kernels[catalog.TargetProductVersion]; want != "" {
		got := zImageKernelVersion(filepath.Join(dsmDir, "zImage"))
		if got != "" && !strings.HasPrefix(got, want) {
			ui.Fail("커널이 %s 것이 아님: %s 를 기대했는데 %s", cfg.Model, want, got)
			fmt.Println("  다른 모델의 DSM 을 받았습니다. 모델을 다시 고르고 설치하세요.")
			bio.Pause("")
			return
		}
		ui.OK("커널 %s (%s 확인)", got, cfg.Model)
	}

	// 3) The driver pack. It has to be in hand before the ramdisk is patched -
	// some of it goes inside the ramdisk, which is what makes the loader disk
	// visible at all.
	//
	// 3) 드라이버 팩. 램디스크 패치보다 먼저 확보해야 한다 - 일부는
	// 램디스크 안에 넣어야 로더 디스크가 보이기 때문이다.
	driverPack := fetchDriverPack(st, plat)
	// Synology's own modules come from the .pat just unpacked. An embedded DSM
	// has no .pat, so its install keeps our builds alone.
	//
	// 시놀로지 자신의 모듈은 방금 푼 .pat 에서 온다. 임베드된 DSM 은 .pat 이 없어
	// 우리 빌드만으로 설치한다.
	if !embedded {
		driverPack = mergePatOriginals(st, driverPack, dsmDir)
	}
	// The downloads and the copies made while merging are garbage now; give
	// them back before the image is built, since everything here lives in RAM.
	//
	// 받은 것과 합치며 생긴 사본은 이제 쓰레기다. 여기서는 모든 것이 RAM 에 있으므로
	// 이미지를 만들기 전에 돌려준다.
	debug.FreeOSMemory()

	// 4) patch
	// 4) 패치
	ui.Section("Patch")
	rdSrc := filepath.Join(dsmDir, "rd.gz")
	raw, err := os.ReadFile(rdSrc)
	if err != nil {
		fmt.Println("read rd.gz:", err)
		bio.Pause("")
		return
	}
	helper, err := initbin.Binary()
	if err != nil {
		fmt.Println("initbin:", err)
		bio.Pause("")
		return
	}
	var extra []ramdisk.File
	if settings := renderSynoinfoTUI(installedSettingsTUI(cfg.Synoinfo)); len(settings) > 0 {
		extra = append(extra, ramdisk.File{Name: ramdisk.SynoinfoName, Mode: 0o644, Data: settings})
	}
	// If the user set the bay order themselves, send it along in the ramdisk.
	// The init early in the boot reads that file and uses it instead of the
	// detection order.
	//
	// 사용자가 베이 순서를 직접 정했으면 램디스크에 실어 보낸다. 부팅
	// 초기의 init 이 이 파일을 읽어 감지 순서 대신 쓴다.
	if plan := renderBayPlan(cfg.Storage.BayOrder); len(plan) > 0 {
		extra = append(extra, ramdisk.File{Name: "vibeldr-bay-plan", Mode: 0o644, Data: plan})
	}
	// The modules that make disks visible go straight into the ramdisk. The
	// driver pack lives on the loader disk, so leaving the driver that opens
	// that disk there too would mean never opening it.
	//
	// 디스크를 보이게 만드는 모듈은 램디스크에 직접 넣는다. 드라이버 팩은
	// 로더 디스크 위에 있어서, 그 디스크를 여는 드라이버까지 거기 두면
	// 영영 못 연다.
	for name, data := range image.RamdiskModules(driverPack, catalog.RamdiskBootstrapModules) {
		extra = append(extra, ramdisk.File{Name: "lib/modules/" + name, Mode: 0o644, Data: data})
	}
	ui.OK("ramdisk 에 부트 모듈 %d 개", len(image.RamdiskModules(driverPack, catalog.RamdiskBootstrapModules)))
	out, rep, err := ramdisk.Patch(raw, helper, extra...)
	if err != nil {
		fmt.Println("ramdisk patch:", err)
		bio.Pause("")
		return
	}
	if err := os.WriteFile(filepath.Join(dsmDir, patchedRamdisk), out, 0o644); err != nil {
		fmt.Println("write initrd:", err)
		bio.Pause("")
		return
	}
	ui.OK("ramdisk patched (build %s)", rep.BuildID)
	if err := patchZImageTUI(dsmDir); err != nil {
		ui.Warn("zImage patch skipped: %v", err)
	}

	// 5) image
	// 5) 이미지
	ui.Section("Image")
	outPath := filepath.Join(cfg.Paths.Output, "loader.img")
	// The layout is read from the disk that is booted right now. The partition
	// slice built here is written onto the same-numbered partition of the real
	// disk as it is, so a size that disagrees does not fit.
	//
	// 배치는 지금 부팅한 디스크에서 읽는다. 여기서 만든 파티션 슬라이스를
	// 실제 디스크의 같은 번호 파티션에 그대로 쓰기 때문에, 크기가 어긋나면
	// 들어가지 않는다.
	//
	// When the table cannot be read, the bootstrap image's own defaults are
	// assumed (cmd/vibeldr bootstrap: -size 1024, -p1 128, -p2 128, -p4 512).
	//
	// 표를 못 읽으면 부트스트랩 이미지의 기본값으로 본다 (cmd/vibeldr bootstrap 의
	// -size 1024, -p1 128, -p2 128, -p4 512).
	totalMB, p1MB, p2MB, p4MB := diskLayoutMB(disk)
	if totalMB == 0 || p1MB == 0 || p2MB == 0 {
		totalMB, p1MB, p2MB, p4MB = 1024, 128, 128, 512
		ui.Warn("디스크 배치를 못 읽어 기본값 사용 (1024/128/128/512)")
	} else {
		ui.Info("디스크 배치: %d MiB (p1 %d / p2 %d / p4 %d)", totalMB, p1MB, p2MB, p4MB)
	}
	builder := &image.Builder{
		TotalMB: totalMB, P1MB: p1MB, P2MB: p2MB, P4MB: p4MB,
		Labels: imageLabels,
	}
	content := &image.Content{}
	// p1 - grub.cfg, loader.yaml, cmdline.txt and identity.cfg are not
	//      rewritten in this pass; only p3 is overwritten, so the boot loader
	//      and the settings are left alone.
	// p2 - the DSM original.
	//
	// p1 - grub.cfg / loader.yaml / cmdline.txt / identity.cfg 는 이 회차에서는
	//      새로 안 쓴다. p3 만 덮어쓰므로 부트로더와 설정은 손대지 않는다.
	// p2 - DSM 원본.
	for _, name := range pat.WantedFiles {
		src := filepath.Join(dsmDir, name)
		if _, err := os.Stat(src); err == nil {
			content.P2 = append(content.P2, image.File{Path: name, Source: src})
		}
	}
	// p3 - the patched kernel and ramdisk.
	// p3 - 패치된 커널 + 램디스크.
	zImage := filepath.Join(dsmDir, patchedZImage)
	if _, err := os.Stat(zImage); err != nil {
		zImage = filepath.Join(dsmDir, "zImage")
	}
	content.P3 = append(content.P3,
		image.File{Path: "zImage-dsm", Source: zImage},
		image.File{Path: patchedRamdisk, Source: filepath.Join(dsmDir, patchedRamdisk)},
	)
	imgRes, err := builder.Build(outPath, content)
	if err != nil {
		fmt.Println("image build:", err)
		bio.Pause("")
		return
	}
	ui.OK("built %s (%s)", outPath, ui.Bytes(imgRes.SizeBytes))

	// 6) Overwrite partition 3.
	// 6) partition 3 덮어쓰기
	ui.Section("Write payload")
	if len(imgRes.Partitions) < 3 {
		bio.Pause("built image has fewer than 3 partitions - aborted")
		return
	}
	p3 := imgRes.Partitions[2]
	f, err := os.Open(outPath)
	if err != nil {
		fmt.Println("open image:", err)
		bio.Pause("")
		return
	}
	defer f.Close()
	p3Bytes := make([]byte, int64(p3.Sectors)*int64(image.SectorSize))
	if _, err := f.ReadAt(p3Bytes, int64(p3.StartLBA)*int64(image.SectorSize)); err != nil {
		fmt.Println("read p3 slice:", err)
		bio.Pause("")
		return
	}
	err = writePartition(disk, 3, p3Bytes, func(done, total int64) {
		ui.ProgressBar("flashing p3", done, total)
	})
	ui.ClearLine()
	if err != nil {
		fmt.Println("flash p3:", err)
		bio.Pause("")
		return
	}
	ui.OK("wrote %s to %s", ui.Bytes(int64(len(p3Bytes))), disk.PartitionDevice(3))

	// 7) Write the driver pack onto partition 4.
	//
	// A Synology kernel carries drivers only for the hardware Synology sells.
	// A machine with a different NIC or controller sees neither network nor
	// disk, and then a DSM install cannot even start. The kernel patch turned
	// signature enforcement off, so the modules put here do load.
	//
	// 7) 드라이버 팩을 파티션 4 에 쓴다.
	//
	// 시놀로지 커널은 시놀로지가 파는 하드웨어의 드라이버만 담고 있다. 다른
	// NIC 이나 컨트롤러를 꽂은 기계는 네트워크도 디스크도 못 잡고, 그러면
	// DSM 설치를 시작할 수조차 없다. 커널 패치로 서명 강제를 껐으므로 여기
	// 실은 모듈이 실제로 로드된다.
	//
	// A failure here is only reported and passed over, because the DSM payload
	// is already on partition 3.
	//
	// 실패해도 DSM 페이로드는 이미 파티션 3 에 있으므로 알리기만 하고 넘어간다.
	// If the pack came off p4 there is no reason to write the same content
	// back. Reassembling it only leaves room for it to differ from the
	// original.
	//
	// 팩을 p4 에서 읽어왔으면 같은 내용을 다시 쓸 이유가 없다. 재조립하면
	// 원본과 달라질 여지만 생긴다.
	if st.packFromDisk {
		ui.OK("드라이버 팩은 이미 파티션 4 에 있음 - 기록 생략")
	} else {
		writeDriverPack(st, driverPack)
		debug.FreeOSMemory()
	}

	// 8) Rewrite the boot menu. The bootstrap grub.cfg knows only
	// vibeldr-boot; now that partition 3 is up to date, partition 1's grub.cfg
	// is rewritten into the three dsm/build/junior entries (or two, without
	// build) so DSM comes up by default from the next boot. A failure is only
	// reported to the user, with an attempt to restore the backup, because the
	// payload is already on partition 3.
	//
	// 8) 부트 메뉴 재작성. 부트스트랩 grub.cfg 는 vibeldr-boot 만 안다.
	// 파티션 3 을 갱신했으니 다음 부팅부터 DSM 이 기본으로 뜨도록 파티션
	// 1 의 grub.cfg 를 dsm/build/junior 3-entry (혹은 build 없는 2-entry)
	// 로 다시 쓴다. 실패해도 payload 는 이미 파티션 3 에 있으므로 사용자에게만
	// 알리고 백업 복원을 시도한다.
	rewriteGrubForDSM(cfg, b.String(), bio)

	// Keep what a kexec jump straight into the kernel and ramdisk just built
	// would need. They are the same files that went onto partition 3, so
	// nothing has to be mounted again.
	//
	// 방금 만든 커널·램디스크로 바로 kexec 점프할 재료를 저장한다.
	// 파티션 3 에 쓴 것과 같은 파일이라 다시 마운트할 필요가 없다.
	kexKernel := filepath.Join(dsmDir, patchedZImage)
	if _, err := os.Stat(kexKernel); err != nil {
		kexKernel = filepath.Join(dsmDir, "zImage")
	}
	st.kexec = kexecPlan{
		kernel:  kexKernel,
		initrd:  filepath.Join(dsmDir, patchedRamdisk),
		cmdline: b.String(),
		ready:   true,
	}

	// Sync and stop there, leaving the reboot to the user: an item on the main
	// menu on the text screen, the reboot button on the last step of the
	// graphical one.
	//
	// sync 까지만 하고 재부팅은 사용자가 고르게 둔다. 글자 메뉴에서는
	// 메인 메뉴의 항목, 그래픽 화면에서는 마지막 단계의 재부팅 단추다.
	syncAll()
	st.built = true
	bio.Pause("완료 - 이제 DSM 으로 재부팅할 수 있습니다")
}

// bootIntoDSM hands over to the DSM that was installed.
//
// With the kexec material ready it jumps straight in and skips the reboot. If
// the kernel blocks kexec - some hypervisors do - or the load fails, it falls
// back to an ordinary reboot; that path, where GRUB loads DSM directly, works
// just as well.
//
// bootIntoDSM - 설치한 DSM 으로 넘어간다.
//
// kexec 재료가 준비돼 있으면 그걸로 바로 점프해 재부팅을 건너뛴다. 커널이
// kexec 를 막거나(예: 일부 하이퍼바이저) 적재가 실패하면 평범한 재부팅으로
// 폴백한다 - GRUB 이 DSM 을 직접 로드하는 그 경로도 정상 동작한다.
func bootIntoDSM(st *tuiState) {
	if st.kexec.ready {
		say("kexec: DSM 커널로 점프 시도")
		if err := kexecIntoDSM(st.kexec.kernel, st.kexec.initrd, st.kexec.cmdline); err != nil {
			say("kexec 실패, 재부팅으로 폴백: %v", err)
		}
	}
	rebootNow()
}

// packOnDisk reads the driver pack already on partition 4.
//
// With a pack put there at build time, installing needs no network at all. An
// empty partition, or one that is not a cpio, comes back as an empty pack and
// the caller downloads as usual.
//
// packOnDisk - 파티션 4 에 이미 들어있는 드라이버 팩을 읽는다.
//
// 빌드할 때 실어 둔 팩이 있으면 설치는 네트워크를 쓸 필요가 없다. 비어 있거나
// cpio 가 아니면 빈 팩을 돌려주고 호출부가 평소대로 받아온다.
func packOnDisk(st *tuiState) kmod.Pack {
	dev := st.disk.PartitionDevice(4)
	f, err := os.Open(dev)
	if err != nil {
		return nil
	}
	defer f.Close()
	// A newc cpio starts with "070701"; an empty partition ends here.
	// cpio(newc) 는 "070701" 로 시작한다. 빈 파티션이면 여기서 끝.
	var magic [6]byte
	if _, err := io.ReadFull(f, magic[:]); err != nil || string(magic[:]) != "070701" {
		return nil
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil
	}
	blob, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	pack, err := image.ModulePackFromCPIO(blob)
	if err != nil {
		ui.Warn("파티션 4 의 팩을 읽지 못함: %v", err)
		return nil
	}
	return pack
}

// fetchDriverPack gets the driver pack for the chosen model.
//
// A Synology kernel carries drivers only for the hardware Synology sells. A
// machine with a different NIC or controller sees neither network nor disk, and
// then a DSM install cannot even start. The kernel patch turned module
// signature enforcement off, so the modules here do load.
//
// A pack already on partition 4 is used when it was built for this model's
// kernel; otherwise the model's pack comes from the latest release of
// catalog.ModuleRepo. A failed download does not stop the build. On a machine
// using only hardware Synology knows, the install works without it.
//
// fetchDriverPack - 고른 모델의 드라이버 팩을 확보한다.
//
// 시놀로지 커널은 시놀로지가 파는 하드웨어의 드라이버만 담고 있다. 다른
// NIC 이나 컨트롤러를 꽂은 기계는 네트워크도 디스크도 못 잡고, 그러면 DSM
// 설치를 시작할 수조차 없다. 커널 패치로 모듈 서명 강제를 껐으므로 여기 모듈이
// 실제로 로드된다.
//
// 파티션 4 에 이미 팩이 있고 이 모델의 커널용이면 그것을 쓰고, 아니면
// catalog.ModuleRepo 의 최신 릴리스에서 모델의 팩을 받는다. 못 받아도 빌드는
// 계속한다. 시놀로지가 아는 하드웨어만 쓰는 기계라면 팩이 없어도 설치가 된다.
func fetchDriverPack(st *tuiState, plat *catalog.Platform) kmod.Pack {
	ui.Section("Drivers")
	if plat == nil {
		ui.Warn("플랫폼을 알 수 없어 드라이버 팩을 건너뜀")
		return nil
	}
	productVer := st.cfg.ProductVersion()
	kernel := plat.Kernels[productVer]
	if kernel == "" {
		ui.Warn("%s 는 DSM %s 커널을 카탈로그에 등록하지 않음 - 건너뜀", plat.Name, productVer)
		return nil
	}

	ui.Field("Target", fmt.Sprintf("%s / DSM %s / kernel %s", plat.Name, productVer, kernel))

	// A pack already on the image is the one used. Putting one there at build
	// time with --driver-pack means the install needs no network for it. The
	// bootstrap image serves every model, so a pack built for another kernel is
	// passed over.
	//
	// 이미지에 팩이 미리 실려 있으면 그걸 쓴다. 빌드할 때 --driver-pack 으로
	// 넣어두면 설치 중 팩 때문에 네트워크를 쓸 일이 없다. 부트스트랩 이미지는 모든
	// 모델용이라, 다른 커널용 팩이면 건너뛴다.
	if pack := packOnDisk(st); len(pack) > 0 {
		rel := kmod.PackRelease(pack)
		if strings.HasPrefix(rel, kernel) {
			ui.OK("이미지에 포함된 드라이버 %d 개 사용 (커널 %s) - 다운로드 생략", pack.Modules(), rel)
			st.packFromDisk = true
			return pack
		}
		ui.Warn("파티션 4 의 팩은 커널 %s 용이라 %s 에 안 맞음 - 새로 받음", rel, kernel)
	}

	name := catalog.ModulePackName(plat.Name, st.cfg.Model)
	ui.Info("%s 의 최신 릴리스에서 %s 받는 중...", catalog.ModuleRepo, name)
	pack, info, err := image.FetchModulePack(context.Background(), st.cfg.Paths.Cache, plat.Name, st.cfg.Model)
	if err != nil {
		ui.Warn("드라이버 팩 %s 를 받지 못함: %v", name, err)
		return nil
	}
	from := info.Tag
	if info.FromCache {
		from += ", 캐시"
	}
	ui.OK("드라이버 %d 개 (%s)", pack.Modules(), from)
	if info.FirmwareErr != nil {
		ui.Warn("펌웨어 팩을 받지 못함: %v - 드라이버만 쓴다", info.FirmwareErr)
	} else {
		ui.OK("펌웨어 %d 개", info.Firmware)
	}
	return pack
}

// writeDriverPack writes the pack onto partition 4 as a raw cpio.
//
// No filesystem is used, because the Synology kernel refuses every vfat mount
// during the ramdisk stage. One archive goes onto the partition whole, and
// during boot `cmd/vibeldr-init` reads it straight off the block device and
// unpacks it in memory.
//
// writeDriverPack - 팩을 파티션 4 에 raw cpio 로 쓴다.
//
// 파일시스템을 쓰지 않는 이유는 시놀로지 커널이 램디스크 단계에서 vfat
// 마운트를 전부 거부하기 때문이다. 아카이브 하나를 파티션에 통째로 쓰고,
// 부팅 중에 `cmd/vibeldr-init` 이 블록 장치에서 직접 읽어 메모리에서 푼다.
func writeDriverPack(st *tuiState, pack kmod.Pack) {
	if len(pack) == 0 {
		return
	}
	blob, err := image.ModulePackCPIO(pack)
	if err != nil {
		ui.Warn("드라이버 팩 조립 실패: %v", err)
		return
	}
	err = writePartition(st.disk, 4, blob, func(done, total int64) {
		ui.ProgressBar("flashing p4", done, total)
	})
	ui.ClearLine()
	if err != nil {
		ui.Warn("파티션 4 쓰기 실패: %v", err)
		return
	}
	ui.OK("wrote %s to %s", ui.Bytes(int64(len(blob))), st.disk.PartitionDevice(4))
}

// rewriteGrubForDSM is buildFlow's last step. It asks the user once and, given
// a yes, rewrites partition 1's grub.cfg so DSM boots by default. On failure it
// tries to restore the bootstrap backup.
//
// rewriteGrubForDSM - buildFlow 마지막 단계. 사용자에게 확인 프롬프트를
// 하나 띄우고, 동의하면 파티션 1 의 grub.cfg 를 DSM 기본 부팅으로 재작성한다.
// 실패 시 부트스트랩 백업으로 복원을 시도한다.
func rewriteGrubForDSM(cfg *config.Config, kernelCmdline string, bio buildIO) {
	ui.Section("Boot menu")
	if _, err := os.Stat(loaderMount); err != nil {
		ui.Warn("파티션 1 이 마운트돼있지 않음 - grub.cfg 재작성 생략")
		return
	}

	// The answer comes back over whichever channel suits the screen. The
	// graphical screen has nowhere to put this question, so it answers empty,
	// and an empty answer counts as Y below.
	//
	// 입력은 화면 종류에 맞는 통로로 받는다. 그래픽 화면에는 이 질문을
	// 받을 자리가 없어서 빈 답이 돌아오고, 빈 답은 아래에서 Y 로 친다.
	ans := strings.ToLower(strings.TrimSpace(bio.Prompt("DSM 로 부팅 기본값 변경 [Y/n]: ")))
	if !(ans == "" || ans == "y" || ans == "yes") {
		ui.Info("grub.cfg 는 그대로 둠 - 다음 부팅도 vibeldr-boot 로 뜬다")
		return
	}

	params := GrubParams{
		Model:         cfg.Model,
		DSMVersion:    cfg.DSM.Version,
		KernelCmdline: kernelCmdline,
		// The reconfigure entry is always rendered. It uses partition 1's
		// Alpine kernel and initrd as they are, so it needs no flag of its own.
		//
		// reconfigure 엔트리는 항상 렌더된다. 파티션 1 의 알파인 커널·initrd
		// 를 그대로 쓰므로 별도 플래그가 없어도 된다.
		Microcode: cfg.Boot.Microcode,
	}
	if err := RewriteGrubConfig(loaderMount, params); err != nil {
		ui.Warn("grub.cfg 재작성 실패: %v", err)
		if rerr := RestoreGrubBackup(loaderMount); rerr != nil {
			ui.Warn("백업 복원도 실패: %v", rerr)
		} else {
			ui.Info("부트스트랩 원본으로 복원됨 - 다음 부팅도 vibeldr-boot")
		}
		return
	}
	ui.OK("grub.cfg 갱신 - 기본 부팅=dsm (원본은 grub.cfg%s 로 백업)", grubBootstrapBackupSuffix)
}

// ---------------------------------------------------------------------------
// helpers - config / identity
// 유틸 - 설정과 identity
// ---------------------------------------------------------------------------

// loadConfigOrDefault tries loader.yaml and falls back to Default() when it is
// missing or will not parse. A first boot comes through here.
//
// loadConfigOrDefault - loader.yaml 을 시도해 보고 없거나 파싱에 실패하면
// Default() 로 간다. 첫 부팅은 이 경로로 흐른다.
func loadConfigOrDefault(cat *catalog.Catalog) *config.Config {
	cfg, err := config.Load(configPath, cat)
	if err == nil {
		return cfg
	}
	if !errors.Is(err, os.ErrNotExist) {
		fmt.Println("loader.yaml load failed, starting from defaults:", err)
	}
	d := config.Default()
	return d
}

// saveConfig writes loader.yaml, ignoring the call silently where the mount
// failed.
//
// saveConfig - loader.yaml 저장. 마운트 실패 상태면 조용히 무시한다.
func saveConfig(cfg *config.Config) error {
	if _, err := os.Stat(loaderMount); err != nil {
		return err
	}
	return cfg.Save(configPath)
}

// resolveIdentityForBuild settles the identity at build time.
//
// The CLI pins its identity with a state.json, but here writing it into the
// config is enough: the next boot reads the same value from the same file.
//
// resolveIdentityForBuild - 빌드 시점의 identity 결정.
//
// CLI 는 state.json 을 두어 identity 를 pin 하지만, 여기서는 config 자체에
// 심는 것으로 충분하다. 다음 부팅에도 같은 파일에서 같은 값을 읽는다.
func resolveIdentityForBuild(cfg *config.Config, cat *catalog.Catalog) (cmdline.Identity, error) {
	id := cmdline.Identity{}
	if cfg.Identity.Serial != "" {
		id.Serial = cfg.Identity.Serial
	} else {
		s, err := cat.GenerateSerial(cfg.Model)
		if err != nil {
			return id, err
		}
		id.Serial = s
	}
	if len(cfg.Identity.MACs) > 0 {
		for _, m := range cfg.Identity.MACs {
			n, err := catalog.NormalizeMAC(m)
			if err != nil {
				return id, err
			}
			id.MACs = append(id.MACs, n)
		}
	} else {
		macs, err := cat.GenerateMACs(cfg.Model, cfg.Identity.NICCount)
		if err != nil {
			return id, err
		}
		id.MACs = macs
	}
	return id, nil
}

// ---------------------------------------------------------------------------
// helpers - patch. The same logic as the CLI's, reimplemented briefly.
// helpers - patch (CLI 것과 동일 로직을 짧게 재구현)
// ---------------------------------------------------------------------------

// patchZImageTUI is the patch that gets past the vmlinux signature check. It
// follows the same path as the CLI's patchZImage, rewritten here because
// cmd/vibeldr is not under internal and cannot be imported. Which sites were
// applied and which were skipped go straight to the console.
//
// patchZImageTUI - vmlinux 서명 검증 우회 패치. CLI 의 patchZImage 와 같은
// 흐름이다. cmd/vibeldr 는 internal 이 아니라 재사용할 수 없어 여기서 다시
// 썼다. 어떤 사이트를 적용하고 어떤 걸 건너뛰었는지 콘솔에 그대로 흘려준다.
func patchZImageTUI(dsmDir string) error {
	src := filepath.Join(dsmDir, "zImage")
	dst := filepath.Join(dsmDir, patchedZImage)
	raw, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	bz, err := kpatch.ParseBzImage(raw)
	if err != nil {
		return err
	}
	vm, err := bz.ExtractVMLinux()
	if err != nil {
		return err
	}
	v, err := kpatch.ParseVMLinux(vm)
	if err != nil {
		return err
	}
	findings, err := kpatch.Analyze(v)
	if err != nil {
		return err
	}
	for _, name := range findings.Missing {
		ui.Warn("kpatch site missing: %s", name)
	}
	if len(findings.Sites) == 0 {
		return fmt.Errorf("no patch sites")
	}
	res := kpatch.Apply(v, findings.Sites)
	for _, s := range res.Applied {
		ui.OK("kpatch %s @ 0x%x (%s)", s.Name, s.FileOff, s.Why)
	}
	for _, sk := range res.Skipped {
		ui.Warn("kpatch %s skipped: %s", sk.Site.Name, sk.Reason)
	}
	if len(res.Applied) == 0 {
		return fmt.Errorf("no patches applied")
	}
	out, err := bz.Rebuild(v.Bytes, compressKernelLZMA)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, out, 0o644)
}

// loaderPolicyTUI are the synoinfo values the loader always writes, the same as
// the CLI's. Anything the user set takes precedence.
//
// loaderPolicyTUI - 로더가 항상 심는 synoinfo 값들 (CLI 것과 동일).
// 사용자가 지정한 값이 우선한다.
var loaderPolicyTUI = map[string]string{
	"support_disk_compatibility": "yes",
	"rss_server":                 "http://127.0.0.1/autoupdate/genRSS.php",
	"rss_server_ssl":             "https://127.0.0.1/autoupdate/genRSS.php",
	"rss_server_v2":              "http://127.0.0.1/autoupdate/v2/getList",
}

func installedSettingsTUI(user map[string]string) map[string]string {
	out := make(map[string]string, len(loaderPolicyTUI)+len(user))
	for k, v := range loaderPolicyTUI {
		out[k] = v
	}
	for k, v := range user {
		out[k] = v
	}
	return out
}

func renderSynoinfoTUI(kv map[string]string) []byte {
	if len(kv) == 0 {
		return nil
	}
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, kv[k])
	}
	return []byte(b.String())
}

// ---------------------------------------------------------------------------
// helpers - I/O
// 유틸 - 입출력
// ---------------------------------------------------------------------------

var stdin = bufio.NewReader(os.Stdin)

// prompt prints a label and reads one line, trimming the CR and LF.
// prompt - 라벨을 출력한 뒤 한 줄을 읽는다. CR/LF 는 잘라낸다.
func prompt(label string) string {
	fmt.Print(label)
	line, err := stdin.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return ""
	}
	return strings.TrimRight(line, "\r\n")
}

func pause(msg string) {
	if msg != "" {
		fmt.Println(msg)
	}
	fmt.Print("[enter] ")
	_, _ = stdin.ReadString('\n')
}

// screenClear sends the VT100 home and clear. This is not real curses, so it
// only works on a scrolling screen that is never partially redrawn.
//
// screenClear - VT100 커서 원점 + 화면 지우기. 실제 curses 는 아니라
// 재그리기가 없는 흘러가는 화면 위에서만 유효하다.
func screenClear() {
	fmt.Print("\x1b[H\x1b[2J")
}

func ifEmpty(s, alt string) string {
	if strings.TrimSpace(s) == "" {
		return alt
	}
	return s
}

func rebootNow() {
	syncAll()
	_ = syscall.Reboot(syscall.LINUX_REBOOT_CMD_RESTART)
	// The reboot should not return; if it does, park here.
	// reboot 이 돌아오면 안 된다. 돌아오면 여기서 멈춰 세운다.
	select {}
}

func syncAll() {
	_ = umountLoaderConfig(loaderMount)
	syscall.Sync()
}

// ---------------------------------------------------------------------------
// rescue - for a failed mount, a failed catalogue load, or an explicit request
// from the user. A very small REPL: ls / cat / ip / dmesg / parts / logs /
// reboot / exit.
//
// rescue - 마운트 실패나 catalog 로드 실패, 또는 사용자가 명시적으로 요청한
// 경우. 초경량 REPL 이고 명령은 위 영문과 같다.
// ---------------------------------------------------------------------------

func rescueLoop() {
	fmt.Println("rescue shell - `help` 로 명령 목록. `exit` 로 뒤로.")
	for {
		line := prompt("rescue> ")
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		switch fields[0] {
		case "exit":
			return
		case "reboot":
			rebootNow()
		case "ip":
			cmdIP()
		case "ls":
			path := "."
			if len(fields) > 1 {
				path = fields[1]
			}
			cmdLs(path)
		case "cat":
			if len(fields) < 2 {
				fmt.Println("usage: cat <path>")
				continue
			}
			cmdCat(fields[1])
		case "dmesg":
			cmdDmesg()
		case "parts":
			cmdParts()
		case "logs":
			if len(fields) < 2 {
				fmt.Println("usage: logs <path>   (try: logs " + logPath + ")")
				continue
			}
			cmdLogs(fields[1])
		case "btrfs":
			// btrfs prints its usage even with no subcommand.
			// btrfs 는 subcommand 없이도 usage 를 찍는다.
			cmdBtrfs(fields[1:])
		case "mount":
			cmdMounts()
		case "lsblk":
			cmdLsblk()
		case "net":
			cmdNet()
		case "journal":
			cmdJournal()
		case "shell":
			cmdShell()
		case "help":
			fmt.Println("commands:")
			fmt.Println("  ls [path]              디렉터리 나열")
			fmt.Println("  cat <path>             파일 앞 64 KiB")
			fmt.Println("  ip                     인터페이스 mac / carrier")
			fmt.Println("  dmesg                  커널 로그 tail")
			fmt.Println("  parts                  블록 디바이스 요약 (sysfs)")
			fmt.Println("  logs <path>            파일 tail 100 줄")
			fmt.Println("  btrfs <sub-command>    btrfs.static 직접 실행")
			fmt.Println("  mount                  /proc/mounts")
			fmt.Println("  lsblk                  /proc/partitions")
			fmt.Println("  net                    인터페이스 + route")
			fmt.Println("  journal                /var/log 파일 목록")
			fmt.Println("  shell                  bash/sh/ash 스폰")
			fmt.Println("  reboot / exit")
		default:
			fmt.Println("unknown command:", fields[0], "  (`help` 참조)")
		}
	}
}

func cmdLs(path string) {
	entries, err := os.ReadDir(path)
	if err != nil {
		fmt.Println("ls:", err)
		return
	}
	for _, e := range entries {
		suffix := ""
		if e.IsDir() {
			suffix = "/"
		}
		fmt.Println(e.Name() + suffix)
	}
}

func cmdCat(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Println("cat:", err)
		return
	}
	defer f.Close()
	// A 64 KiB cap, so that hitting a large binary file by accident does not
	// kill the console.
	//
	// 커다란 이진 파일에 잘못 눌러도 콘솔이 죽지 않게 64 KiB 상한을 둔다.
	buf := make([]byte, 64*1024)
	n, _ := f.Read(buf)
	os.Stdout.Write(buf[:n])
	if n == cap(buf) {
		fmt.Println()
		fmt.Println("(truncated at 64 KiB)")
	}
}

func cmdIP() {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		fmt.Println("ip:", err)
		return
	}
	for _, e := range entries {
		if e.Name() == "lo" {
			continue
		}
		mac, _ := os.ReadFile(filepath.Join("/sys/class/net", e.Name(), "address"))
		carrier, _ := os.ReadFile(filepath.Join("/sys/class/net", e.Name(), "carrier"))
		fmt.Printf("%s: mac=%s carrier=%s\n", e.Name(),
			strings.TrimSpace(string(mac)), strings.TrimSpace(string(carrier)))
	}
}

// cmdDmesg prints the tail of the kernel log ring buffer, around the last 50
// lines. klogctl(3, buf, len) reads the whole buffer, which is small enough to
// take entirely and then tail.
//
// cmdDmesg - 커널 로그 링버퍼의 마지막 부분 (약 마지막 50 줄) 을 찍는다.
// klogctl(3, buf, len) 은 링버퍼 전체를 읽어온다. 크기가 작으니 통째로
// 받아서 tail 처리한다.
func cmdDmesg() {
	buf := make([]byte, 256*1024)
	n, err := syscall.Klogctl(3, buf) // SYSLOG_ACTION_READ_ALL / 링버퍼 전체 읽기
	if err != nil {
		fmt.Println("dmesg:", err)
		return
	}
	// Keep the last N lines only.
	// 마지막 N 줄만 남긴다.
	lines := bytes.Split(buf[:n], []byte{'\n'})
	const want = 50
	if len(lines) > want {
		lines = lines[len(lines)-want:]
	}
	for _, l := range lines {
		os.Stdout.Write(l)
		os.Stdout.Write([]byte{'\n'})
	}
}

// cmdParts walks /sys/class/block and prints the partitions and their sizes:
// kernel name, size, parent disk.
//
// cmdParts - /sys/class/block 을 훑어 파티션과 크기를 출력한다.
// 커널 이름 / 크기 / 부모 디스크를 나열한다.
func cmdParts() {
	entries, err := os.ReadDir("/sys/class/block")
	if err != nil {
		fmt.Println("parts:", err)
		return
	}
	// Disks first, then the partitions.
	// 디스크 먼저, 그 다음 파티션 순으로.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, e := range entries {
		name := e.Name()
		base := filepath.Join("/sys/class/block", name)
		sizeRaw, _ := os.ReadFile(filepath.Join(base, "size"))
		var sectors int64
		fmt.Sscanf(strings.TrimSpace(string(sizeRaw)), "%d", &sectors)
		bytesSize := sectors * 512

		partRaw, _ := os.ReadFile(filepath.Join(base, "partition"))
		partNo := strings.TrimSpace(string(partRaw))

		kind := "disk"
		if partNo != "" {
			kind = "part" + partNo
		}
		fmt.Printf("%-14s %-6s %s\n", name, kind, ui.Bytes(bytesSize))
	}
}

// cmdLogs is the last 100 lines of a given file, useful for going through a
// session log such as /tmp/vibeldr.log. For a large file only the last 256 KiB
// is read.
//
// cmdLogs - 주어진 파일의 마지막 100 줄. /tmp/vibeldr.log 같은 세션 로그를
// 훑을 때 쓴다. 파일이 크면 뒤쪽 256 KiB 만 읽는다.
func cmdLogs(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Println("logs:", err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fmt.Println("logs:", err)
		return
	}
	const window = 256 * 1024
	start := int64(0)
	if st.Size() > window {
		start = st.Size() - window
	}
	buf := make([]byte, st.Size()-start)
	if _, err := f.ReadAt(buf, start); err != nil && !errors.Is(err, io.EOF) {
		fmt.Println("logs:", err)
		return
	}
	lines := bytes.Split(buf, []byte{'\n'})
	// The front may have been cut off, so the first, partial line is dropped.
	// 앞이 잘렸을 수 있으니 첫 (부분) 라인은 버린다.
	if start > 0 && len(lines) > 0 {
		lines = lines[1:]
	}
	const want = 100
	if len(lines) > want {
		lines = lines[len(lines)-want:]
	}
	for _, l := range lines {
		os.Stdout.Write(l)
		os.Stdout.Write([]byte{'\n'})
	}
}

// listEmbeddedDSM is the list of models whose decrypted kernel is on the
// image's P2.
//
// It is read once, when the wizard starts. Picking one of these models makes
// the install skip the download and the decryption entirely, which the screen
// says so too.
//
// listEmbeddedDSM - 이미지 P2 에 복호화된 커널이 들어있는 모델 목록.
//
// 마법사가 시작할 때 한 번만 읽는다. 여기 있는 모델을 고르면 설치가
// 다운로드와 복호화를 통째로 건너뛰므로, 화면에도 그렇게 알려준다.
func listEmbeddedDSM(st *tuiState) map[string]bool {
	const p2Mount = "/mnt/embed"
	out := map[string]bool{}
	if st == nil || st.disk.Kernel == "" {
		return out
	}
	if err := mountPartition(st.disk, 2, p2Mount); err != nil {
		return out
	}
	defer syscall.Unmount(p2Mount, 0)

	entries, err := os.ReadDir(filepath.Join(p2Mount, "dsm"))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(p2Mount, "dsm", e.Name())
		ok := true
		for _, f := range []string{"zImage", "rd.gz", "VERSION"} {
			if fi, err := os.Stat(filepath.Join(dir, f)); err != nil || fi.IsDir() {
				ok = false
				break
			}
		}
		if ok {
			out[e.Name()] = true
		}
	}
	return out
}

// useEmbeddedDSM copies this model's decrypted kernel from the image's P2 into
// the work directory, if it is there. True means it was, and the caller skips
// the download and the decryption.
//
// The bootstrap builder puts them on P2 as "dsm/<model>/{zImage,rd.gz,VERSION}".
// P2 is mounted briefly, the three files are copied into dsmDir, and it is
// unmounted. All three have to be there to count as success; one missing and it
// falls back to downloading as usual.
//
// useEmbeddedDSM - 이미지 P2 에 이 모델의 복호화된 커널이 있으면 작업
// 디렉터리로 복사한다. 있으면 true 이고, 호출자는 다운로드·복호화를 건너뛴다.
//
// 부트스트랩 빌더가 P2 에 "dsm/<모델>/{zImage,rd.gz,VERSION}" 로 심어 둔다.
// P2 를 잠깐 마운트해 세 파일을 dsmDir 로 복사하고 뗀다. 세 파일이 다
// 있어야 성공으로 본다 - 하나라도 없으면 평소대로 다운로드로 떨어진다.
func useEmbeddedDSM(st *tuiState, model, dsmDir string) bool {
	const p2Mount = "/mnt/embed"
	if err := mountPartition(st.disk, 2, p2Mount); err != nil {
		return false
	}
	defer syscall.Unmount(p2Mount, 0)

	srcDir := filepath.Join(p2Mount, "dsm", model)
	want := []string{"zImage", "rd.gz", "VERSION"}
	for _, f := range want {
		if st, err := os.Stat(filepath.Join(srcDir, f)); err != nil || st.IsDir() {
			return false
		}
	}
	if err := os.MkdirAll(dsmDir, 0o755); err != nil {
		return false
	}
	for _, f := range want {
		if err := copyFile(filepath.Join(srcDir, f), filepath.Join(dsmDir, f)); err != nil {
			return false
		}
	}
	return true
}

// copyFile copies src to dst.
// copyFile - src 를 dst 로 복사한다.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
