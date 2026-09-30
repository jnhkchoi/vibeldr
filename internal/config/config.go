// Package config is loader.yaml: the one declarative description of a loader
// build.
//
// This file is the only input a build reads. Everything derived from it goes
// into state.json, and state.json is written by the tool rather than edited by
// a person.
//
// Package config - loader.yaml. 로더 빌드의 유일한 선언적 서술.
//
// 이 파일 하나가 빌드가 읽는 유일한 입력이다. 파생물은 전부 state.json 에
// 들어가고, state.json 은 툴이 쓰는 것이지 사람이 손대는 게 아니다.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"

	"vibeldr/internal/catalog"
)

// SchemaVersion is the schema number a loader.yaml has to declare, fixed at 1.
// Schema changes may only add fields - an existing field never changes meaning
// - so there is no reason to raise it. A config carrying anything else is
// rejected rather than quietly misread.
//
// SchemaVersion - loader.yaml 이 준수해야 하는 스키마 번호. 값은 1 로 고정.
// 스키마 변경은 필드 추가만 허용하고 (기존 필드의 뜻은 바뀌지 않음) 그래서
// 이 값을 올릴 이유가 없다. 이보다 낮거나 높은 값을 담은 config 는 조용히
// 오독하지 않고 거부한다.
const SchemaVersion = 1

type Config struct {
	Version int `yaml:"version"`

	Model string `yaml:"model"`
	DSM   DSM    `yaml:"dsm"`

	Identity Identity `yaml:"identity"`
	Boot     Boot     `yaml:"boot"`
	Storage  Storage  `yaml:"storage"`

	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은 없다.
	Extensions []string `yaml:"extensions"`
	Modules    Modules  `yaml:"modules"`

	// Notify is how boot status is reported outwards. Both channels are
	// optional, and with neither set vibeldr-init does not even try. A failure
	// never stops the boot: it is best-effort.
	//
	// Notify - 부팅 상태를 외부로 알리는 채널. 두 채널 모두 옵션이며, 아무
	// 것도 지정하지 않으면 vibeldr-init 은 알림을 시도조차 하지 않는다.
	// 실패해도 부팅은 계속된다 (best-effort).
	Notify Notify `yaml:"notify"`

	// Synoinfo entries are written into the DSM ramdisk's synoinfo.conf.
	// Synoinfo 항목은 DSM 램디스크의 synoinfo.conf 에 쓰인다.
	Synoinfo map[string]string `yaml:"synoinfo"`
	// Cmdline holds extra kernel parameters. A value of "" produces a bare flag.
	// Cmdline - 추가 커널 파라미터. 값이 "" 이면 값 없는 플래그로 나간다.
	Cmdline map[string]string `yaml:"cmdline"`

	Paths Paths `yaml:"paths"`
}

type DSM struct {
	// Version is the full DSM build, e.g. "7.4.1-90080".
	// Version - DSM 전체 빌드 문자열 ("7.4.1-90080" 등).
	Version string `yaml:"version"`
	// URL and MD5 may be left empty; they are then resolved from the catalog
	// or from an explicit `vibeldr fetch --url`.
	//
	// URL 과 MD5 는 비워 둬도 된다. 그러면 카탈로그나 명시적인
	// `vibeldr fetch --url` 에서 채워진다.
	URL string `yaml:"url"`
	MD5 string `yaml:"md5"`
}

type Identity struct {
	// Serial is generated from the model's rule when empty.
	// Serial - 비어 있으면 모델 규칙에 따라 생성된다.
	Serial string `yaml:"serial"`
	// MACs are 12 hex digit addresses. When empty, NICCount addresses are
	// generated. The count of this list always wins over NICCount when set.
	//
	// MACs - 16진수 12자리 주소들. 비어 있으면 NICCount 개를 생성한다. 목록이
	// 채워져 있으면 그 개수가 NICCount 보다 우선한다.
	MACs []string `yaml:"macs"`
	// NICCount is how many NICs to declare to DSM when MACs is empty.
	// NICCount - MACs 가 비었을 때 DSM 에 알릴 랜카드 개수.
	NICCount int `yaml:"nic_count"`
	// NICOrder is the network cards in LAN order - LAN 1 (eth0) first - each
	// named by hwscan.NICKey, and MACs follows the same order. The wizard fills
	// it in; left empty, DSM's own driver-load order stays.
	//
	// NICOrder - LAN 순서의 랜카드 목록. LAN 1(eth0)이 먼저이고, 카드마다
	// hwscan.NICKey 로 부르며, MACs 도 같은 순서를 따른다. 마법사가 채운다.
	// 비워 두면 DSM 자신의 드라이버 로드 순서가 남는다.
	NICOrder []string `yaml:"nic_order"`
	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// No kernel or ramdisk of these models reads vid= or pid=.
	//
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은
	// 없다. 이 모델들의 커널도 램디스크도 vid= 와 pid= 를 읽지 않는다.
	VID string `yaml:"vid"`
	PID string `yaml:"pid"`

	// ForceMAC hands the addresses in MACs to the network cards, instead of
	// letting them keep the addresses they were built with.
	//
	// Off by default, which is the quiet choice: a card keeps its own address
	// and everything on the network that was arranged around it - a DHCP
	// reservation, a firewall rule, a switch port - keeps working. Set MACs to
	// what the cards already have and DSM and the hardware simply agree.
	//
	// Turn it on when the address is the point rather than an accident: a
	// virtual machine that has to come back with the same address after being
	// rebuilt, or a move onto different hardware that has to look the same
	// from the network's side.
	//
	// ForceMAC - MACs 의 주소를 랜카드에 실제로 박는다. 카드가 원래 갖고
	// 있던 주소를 그대로 쓰게 두지 않는다.
	//
	// 기본은 꺼짐이고 그게 조용한 선택이다. 카드가 자기 주소를 유지하면
	// 그 주소를 전제로 짜 둔 것들 - DHCP 예약, 방화벽 규칙, 스위치 포트 -
	// 이 그대로 동작한다. MACs 에 카드가 이미 가진 주소를 적어 두면 DSM 과
	// 하드웨어가 그냥 일치한다.
	//
	// 켜야 할 때는 주소가 우연이 아니라 목적일 때다. 재구성 후에도 같은
	// 주소로 돌아와야 하는 가상 머신이라든가, 네트워크 쪽에서 보기에 똑같아야
	// 하는 다른 하드웨어로의 이사 같은 경우다.
	ForceMAC bool `yaml:"force_mac"`
}

type Boot struct {
	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// The loader decides between kexec and a reboot from the hypervisor it
	// runs on (cmd/vibeldr-boot bootIntoDSM).
	//
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은
	// 없다. kexec 와 재부팅 중 무엇을 쓸지는 로더가 자기가 도는 하이퍼바이저를
	// 보고 정한다 (cmd/vibeldr-boot 의 bootIntoDSM).
	Method string `yaml:"method"`
	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은 없다.
	SATADOM int `yaml:"satadom"`
	// KernelPanic is the seconds before an automatic reboot after a panic.
	// 0 means never reboot, -1 means reboot immediately.
	//
	// KernelPanic - 패닉 후 자동 재부팅까지의 초. 0 은 재부팅 안 함,
	// -1 은 즉시 재부팅.
	KernelPanic int `yaml:"kernel_panic"`
	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은 없다.
	IPWaitSeconds int `yaml:"ip_wait_seconds"`
	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// The DSM kernels of these models have no screen console to blank.
	//
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은
	// 없다. 이 모델들의 DSM 커널에는 끌 화면 콘솔이 없다.
	ConsoleBlank int `yaml:"console_blank"`
	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// The DSM kernels of these models have no early microcode loader.
	//
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은
	// 없다. 이 모델들의 DSM 커널에는 조기 마이크로코드 로더가 없다.
	Microcode bool `yaml:"microcode"`
}

type Storage struct {
	// SataPortMap and DiskIdxMap apply to non-DT platforms only.
	// SataPortMap 과 DiskIdxMap 은 비-DT 플랫폼에만 적용된다.
	SataPortMap string `yaml:"sata_portmap"`
	DiskIdxMap  string `yaml:"disk_idx_map"`
	// SataRemap is the alternative form: "0>4:1>5".
	// SataRemap - 대체 표기 형식. "0>4:1>5" 처럼 쓴다.
	SataRemap string `yaml:"sata_remap"`
	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은 없다.
	USBAsInternal bool `yaml:"usb_as_internal"`

	// NCQ turns command queueing on. It is off by default, and that default
	// is about virtual machines rather than about performance.
	//
	// QEMU's emulated SATA controller accepts queued commands but does not
	// implement the log page the kernel reads when one of them fails, so the
	// kernel sees a string of aborted reads, turns queueing off itself, and
	// leaves the disk marked with errors:
	//
	//	ata7: failed to read log page 10h (errno=-5)
	//	ata7.00: NCQ disabled due to excessive errors
	//	ata7.00: failed command: READ FPDMA QUEUED
	//
	// DSM reads those errors as a failing drive. Asking for queueing on a
	// controller that cannot report queued failures buys nothing; on real
	// hardware it is worth having, and this turns it back on.
	//
	// NCQ - 커맨드 큐잉을 켠다. 기본이 꺼짐인 이유는 성능이 아니라 가상
	// 머신 때문이다.
	//
	// QEMU 의 에뮬레이션 SATA 컨트롤러는 큐잉된 커맨드를 받아들이지만, 그중
	// 하나가 실패했을 때 커널이 읽는 로그 페이지를 구현하지 않았다. 그래서
	// 커널은 중단된 읽기가 줄줄이 이어지는 것을 보고 스스로 큐잉을 끄며,
	// 디스크에는 오류 표시가 남는다 (위 로그). DSM 은 그 오류를 죽어 가는
	// 드라이브로 읽는다. 큐잉 실패를 보고하지 못하는 컨트롤러에 큐잉을
	// 요구해서 얻는 건 없다. 실기에서는 켤 가치가 있고, 이 옵션이 그걸
	// 다시 켠다.
	NCQ bool `yaml:"ncq"`

	// BayOrder is an explicit choice of which disk goes in which bay.
	//
	// Each entry is "<PCIe path> <port number>", and the order they are listed
	// in is bay 1, 2, 3 and so on. A port that is not in the list is not
	// visible to DSM.
	//
	// Empty means bays are filled in the order detection found them. Automatic
	// placement treats a controller as a single block, so this list is what
	// overrides it when controllers are mixed, or when a particular disk has to
	// stay in a particular bay.
	//
	// BayOrder - 디스크를 어느 베이에 놓을지 직접 정한 순서.
	//
	// 각 항목은 "<PCIe 경로> <포트 번호>" 이고, 나열한 순서가 곧 베이 1,
	// 2, 3 … 이 된다. 목록에 없는 포트는 DSM 에 보이지 않는다.
	//
	// 비어 있으면 부팅 때 감지한 순서대로 자동 배치한다. 자동 배치는
	// 컨트롤러를 하나의 덩어리로 다루기 때문에, 컨트롤러가 섞여 있거나
	// 특정 디스크를 특정 베이에 고정하고 싶을 때 이 목록으로 덮어쓴다.
	BayOrder []string `yaml:"bay_order"`

	// Bays overrides the model's default bay count (see MaxBays). 0 keeps the
	// model default.
	//
	// Bays - 모델 기본 베이 수를 덮어쓴다 (MaxBays 참고). 0 이면 모델 기본을
	// 그대로 쓴다.
	Bays int `yaml:"bays"`
	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은 없다.
	NVMeSlots int `yaml:"nvme_slots"`
	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은 없다.
	Expansion []ExpansionUnit `yaml:"expansion"`
	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은 없다.
	SAS bool `yaml:"sas"`

	// Retired: still accepted so an older loader.yaml loads; nothing reads it.
	// 더 쓰지 않는다. 옛 loader.yaml 이 그대로 읽히도록 받아만 두고, 읽는 곳은 없다.
	NVMeSystem bool `yaml:"nvme_system"`
}

// ExpansionUnit is the shape of the retired storage.expansion entries.
// ExpansionUnit - 더 쓰지 않는 storage.expansion 항목의 모양.
type ExpansionUnit struct {
	Kind string `yaml:"kind"`
	Bays int    `yaml:"bays"`
}

type Modules struct {
	// Blacklist entries are passed to modprobe.blacklist.
	// Blacklist 항목은 modprobe.blacklist 로 넘어간다.
	Blacklist []string `yaml:"blacklist"`
}

// Notify configures boot-event notification. Both channels run in parallel.
// Notify - 부팅 이벤트 알림 설정. 두 채널을 병렬로 지원한다.
type Notify struct {
	// WebhookURL is a Discord, Slack, Telegram or plain HTTP endpoint. Empty
	// skips the webhook. The POST body is JSON.
	//
	// WebhookURL - Discord/Slack/Telegram/일반 HTTP 엔드포인트. 비어 있으면
	// 웹훅 전송을 건너뛴다. POST 본문은 JSON 이다.
	WebhookURL string `yaml:"webhook_url"`
	// Email is a plain-text message over SMTP. Any required field left empty
	// skips the mail: a partial configuration is silence rather than an error.
	//
	// Email - SMTP 를 통한 텍스트 메일. 필수 필드 하나라도 비어 있으면 메일
	// 전송을 건너뛴다. 부분 설정은 오류가 아니라 침묵이다.
	Email NotifyEmail `yaml:"email"`
}

// JSON is n in the form the ramdisk carries it to the helper
// (cmd/vibeldr-init/notify.go), or nil when no channel would send anything:
// no webhook, and the mail lacking its server, sender or recipient.
//
// JSON - 램디스크가 헬퍼에게 실어 가는 형식의 n (cmd/vibeldr-init/notify.go).
// 보낼 채널이 없으면 nil 이다. 웹훅이 없고, 메일에 서버·발신·수신 중 하나가
// 빠진 경우다.
func (n Notify) JSON() []byte {
	e := n.Email
	if n.WebhookURL == "" && (e.SMTP == "" || e.From == "" || e.To == "") {
		return nil
	}
	type email struct {
		SMTP     string `json:"smtp"`
		From     string `json:"from"`
		To       string `json:"to"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	out, err := json.Marshal(struct {
		WebhookURL string `json:"webhook_url"`
		Email      email  `json:"email"`
	}{n.WebhookURL, email(e)})
	if err != nil {
		return nil
	}
	return out
}

// NotifyEmail is the minimum needed to send mail over STARTTLS SMTP. Username
// and Password may be empty for a relay that needs no authentication.
//
// NotifyEmail - STARTTLS SMTP 로 보낼 메일의 최소 정보. 인증이 필요없는
// 릴레이면 Username/Password 는 비워도 된다.
type NotifyEmail struct {
	SMTP     string `yaml:"smtp"`     // host:port, e.g. smtp.gmail.com:587 / 예: smtp.gmail.com:587
	From     string `yaml:"from"`     // sender address / 발신 주소
	To       string `yaml:"to"`       // recipients, comma separated / 수신 주소 (쉼표로 여러 명)
	Username string `yaml:"username"` // AUTH user; empty sends unauthenticated / 비어있으면 인증 없이 전송
	Password string `yaml:"password"` // AUTH password / AUTH 비밀번호
}

type Paths struct {
	Work   string `yaml:"work"`
	Cache  string `yaml:"cache"`
	Output string `yaml:"output"`
}

// Default returns a config with every optional field filled in, so that a
// freshly written loader.yaml documents the real defaults rather than hiding
// them in code.
//
// DSM.Version is left empty here. cmdInit fills it in, with its URL and MD5,
// from the release catalog for the chosen model.
//
// Default - 모든 옵션 필드가 채워진 config 를 반환한다. 갓 쓴 loader.yaml 이
// 코드에 감춰진 값 대신 진짜 기본값을 문서화하도록.
//
// DSM.Version 은 여기서 비워 둔다. cmdInit 가 고른 모델에 맞게 릴리스
// 카탈로그에서 URL, MD5 와 함께 채워 넣는다.
func Default() *Config {
	return &Config{
		Version: SchemaVersion,
		Model:   "SA6400",
		DSM:     DSM{},
		Identity: Identity{
			NICCount: 1,
		},
		Boot: Boot{
			KernelPanic: 5,
		},
		Modules: Modules{
			// Only modules that misbehave on real hardware belong here, and
			// never on suspicion. A driver that crashes the kernel means the
			// module pack was built against the wrong .config - the build is
			// what gets fixed, not this list.
			//
			// evbug dumps every input event into the kernel log and buries the
			// boot messages.
			//
			// cdc_ether is not here: it is DSM's own driver for USB network
			// cards of the CDC ECM kind, rndis_host and r8153_ecm build on it,
			// and its ID table already leaves the Realtek 8152/8153 to r8152.
			//
			// 실기에서 문제를 일으키는 것만 넣고, 짐작으로는 넣지 않는다.
			// 드라이버가 커널을 죽인다면 모듈 팩을 틀린 .config 로 빌드했다는
			// 뜻이므로, 고칠 곳은 이 목록이 아니라 빌드다.
			//
			// evbug 는 모든 입력 이벤트를 커널 로그로 쏟아 부팅 메시지를 묻는다.
			//
			// cdc_ether 는 넣지 않는다. CDC ECM 방식 USB 랜카드용 DSM 자신의
			// 드라이버이고, rndis_host 와 r8153_ecm 이 그 위에 서며, 그 ID 표가 이미
			// Realtek 8152/8153 을 r8152 에 넘긴다.
			Blacklist: []string{"evbug"},
		},
		// maxdisks is deliberately not written here. The bay count belongs to
		// the model, and a fixed 16 would be read as 16 on a 4-bay model and
		// throw DiskIdxMap out. MaxBays() gets it from the model instead.
		//
		// The values below are switches that stop DSM looking for hardware
		// this machine does not have. Looking for it floods the log (oob_ctl),
		// rejects perfectly good disks and memory (the compatibility checks),
		// or takes hold of fan and LED controllers that are not there.
		//
		// They are written to the installed system on every boot, because a
		// DSM update replaces this file (see cmd/vibeldr-init/settings.go).
		//
		// maxdisks 는 여기 적지 않는다. 베이 수는 모델이 정하는 값이고,
		// 고정 16 을 넣어두면 4 베이 모델에서도 16 으로 읽혀 DiskIdxMap 이
		// 어긋난다. MaxBays() 가 모델에서 끌어온다.
		//
		// 아래 값들은 "이 기계에 없는 하드웨어를 DSM 이 찾지 않게" 하는
		// 스위치다. 없는 걸 찾으면 로그가 도배되거나(oob_ctl), 멀쩡한
		// 디스크·메모리를 거부하거나(disk/memory compatibility), 있지도 않은
		// 팬·LED 컨트롤러를 붙잡는다.
		//
		// 매 부팅마다 설치된 시스템에 다시 쓴다. DSM 업데이트가 이 파일을
		// 갈아치우기 때문이다 (cmd/vibeldr-init/settings.go 참고).
		Synoinfo: map[string]string{
			// The disk compatibility check is turned ON. That is the opposite
			// of the usual assumption, but in DSM 7.4's rule engine (v2) it is
			// turning it off that causes the problem.
			//
			//	"no"  → the verdict stays "disabled". The rule file
			//	        (rule_v2_host_<model>.db) has no rule for disabled, so
			//	        "No matching rule found for disk" appears and the whole
			//	        pool drops to at_risk_high. Storage Manager shows the
			//	        drives as "unverified" and the pool as "at risk".
			//	"yes" → DSM looks the drive up in the compatibility DB. This
			//	        machine's drives are put there as "support" (hddb.go),
			//	        so rule [9] compatibility=support → pool_selectable=yes
			//	        applies and they show up as normal.
			//
			// So the point is not to switch the check off but to let the
			// drives pass it.
			//
			// 디스크 호환성 검사는 **켠다**. 통념과 반대인데, DSM 7.4 의
			// 규칙 엔진(v2)에서는 끄는 쪽이 오히려 문제를 만든다.
			//
			//	"no"  → 판정이 "disabled" 로 남는다. 규칙 파일
			//	        (rule_v2_host_<model>.db) 에는 disabled 용 규칙이
			//	        없어서 "No matching rule found for disk" 가 뜨고,
			//	        풀 전체가 at_risk_high 로 떨어진다. 저장소 관리자에
			//	        드라이브는 "미검증", 풀은 "위험".
			//	"yes" → DSM 이 호환성 DB 를 조회한다. 우리가 이 기계의
			//	        드라이브를 거기 "support" 로 넣어두므로 (hddb.go)
			//	        규칙 [9] compatibility=support → pool_selectable=yes
			//	        가 걸려 정상으로 뜬다.
			//
			// 즉 "검사를 끄는" 게 아니라 "검사를 통과시키는" 게 맞다.
			"support_disk_compatibility": "yes",
			// Memory has no rule engine, so turning it off works as expected.
			// 메모리는 규칙 엔진이 없어서 끄는 쪽이 그대로 통한다.
			"support_memory_compatibility": "no",
			"support_memory_limitation":    "no",
			// Management hardware that is not here. Leaving support_oob_ctl on
			// makes scemd pour "Read OOB reg NNN failed" into the log several
			// times a second.
			//
			// 없는 관리 하드웨어. support_oob_ctl 을 안 끄면 scemd 가
			// "Read OOB reg NNN failed" 를 초당 여러 번 로그에 쏟는다.
			"support_oob_ctl": "no",
			// Fan, LED and buzzer controllers that are not here.
			// 없는 팬 컨트롤러 / LED 컨트롤러 / 부저.
			"support_fan":                       "no",
			"support_fan_adjust_dual_mode":      "no",
			"supportadt7490":                    "no",
			"support_led_brightness_adjustment": "no",
			"support_leds_atmega1608":           "no",
			"support_leds_lp3943":               "no",
			"buzzeroffen":                       "0xffff",
			// The ones worth leaving on.
			// 켜두는 쪽이 이득인 것들.
			"supportext4":         "yes",
			"support_uasp":        "yes",
			"support_printer":     "yes",
			"support_usb_printer": "yes",
			"enableRCPower":       "yes",
		},
		Cmdline: map[string]string{},
		// Forward slashes keep the generated YAML readable on every platform;
		// Go accepts them as path separators on Windows too.
		//
		// 슬래시로 쓰면 어느 플랫폼에서도 생성된 YAML 이 읽기 좋다. Go 는
		// 윈도우에서도 슬래시를 경로 구분자로 받아들인다.
		Paths: Paths{
			Work:   "work",
			Cache:  "work/cache",
			Output: "work/out",
		},
	}
}

// applyDefaults fills in the zero values the user left out of the file. It runs
// before validation, so a field with a sensible default is never reported as
// missing.
//
// applyDefaults - 사용자가 파일에서 빠뜨린 zero 값을 채운다. validation 앞에
// 돌기 때문에, 합리적 기본값이 있는 필드가 "빠졌음" 으로 오탐되지 않는다.
func (c *Config) applyDefaults() {
	d := Default()
	if c.Version == 0 {
		c.Version = SchemaVersion
	}
	if c.Identity.NICCount == 0 {
		c.Identity.NICCount = len(c.Identity.MACs)
		if c.Identity.NICCount == 0 {
			c.Identity.NICCount = d.Identity.NICCount
		}
	}
	if c.Paths.Work == "" {
		c.Paths.Work = d.Paths.Work
	}
	if c.Paths.Cache == "" {
		c.Paths.Cache = c.Paths.Work + "/cache"
	}
	if c.Paths.Output == "" {
		c.Paths.Output = c.Paths.Work + "/out"
	}
	// Missing synoinfo keys are filled in from the defaults instead of the map
	// being left as the file found it. A loader.yaml without a synoinfo
	// section would otherwise wipe every default the moment it is read, and
	// the loader reads p1's loader.yaml on every boot, so a saved file keeps
	// winning. Keys the user wrote are left alone; only absent ones are added.
	//
	// synoinfo 는 파일에 있는 그대로 두지 않고 빠진 키를 기본값으로 채운다.
	// synoinfo 항목이 없는 loader.yaml 을 읽으면 기본값이 통째로 사라지는데,
	// 로더는 부팅할 때마다 p1 의 loader.yaml 을 읽으므로 한 번 저장된 파일이
	// 계속 이긴다. 사용자가 쓴 키는 건드리지 않고 없는 키만 채운다.
	if c.Synoinfo == nil {
		c.Synoinfo = map[string]string{}
	}
	for k, v := range d.Synoinfo {
		if _, ok := c.Synoinfo[k]; !ok {
			c.Synoinfo[k] = v
		}
	}
	if c.Cmdline == nil {
		c.Cmdline = map[string]string{}
	}
}

// MaxBays is the internal bay count this configuration recognises.
//
// In order of precedence:
//
//  1. storage.bays - a value the user narrowed deliberately. Strongest.
//  2. the model's real maxdisks - the truth, for a model the catalog knows.
//
// With neither it is 0. The caller must treat 0 as "unknown" and not
// fill it with an arbitrary number: a wrong bay count throws DiskIdxMap out
// entirely.
//
// MaxBays - 이 설정이 인정하는 내장 베이 수.
//
// 우선순위:
//
//  1. storage.bays  - 사용자가 일부러 좁힌 값. 가장 세다.
//  2. 모델의 실제 maxdisks - 카탈로그가 아는 모델이면 이게 진실이다.
//
// 둘 다 없으면 0 이다. 호출자는 0 을 "모른다" 로 다루어야 하고, 임의의 수로
// 메우면 안 된다. 틀린 베이 수는 DiskIdxMap 을 통째로 어긋나게 만든다.
func (c *Config) MaxBays() int {
	if c.Storage.Bays > 0 {
		return c.Storage.Bays
	}
	if n := catalog.BaysOf(c.Model); n > 0 {
		return n
	}
	return 0
}

// Load reads loader.yaml and validates it.
// Load - loader.yaml 을 읽고 검증한다.
func Load(path string, cat *catalog.Catalog) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var c Config
	// KnownFields makes a typo in a key an error instead of a silently ignored
	// setting - the failure mode behind "I changed it but nothing happened",
	// which is hard to debug.
	//
	// KnownFields 덕분에 키 오타가 조용히 무시되는 설정이 아니라 오류가 된다.
	// "고쳤는데 아무 일도 안 일어난다" 를 찾기 어렵게 만드는 실패 양상이 바로
	// 그것이다.
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.Validate(cat); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes the config back out with a header comment.
// Save - 헤더 코멘트를 붙여 config 를 다시 쓴다.
func (c *Config) Save(path string) error {
	var sb strings.Builder
	sb.WriteString("# vibeldr loader configuration\n")
	sb.WriteString("# This file is the only input to a build. Everything else is derived.\n")
	sb.WriteString("# Docs: README.md\n\n")

	out, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	sb.Write(out)

	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(path, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

var (
	dsmVersionPattern = regexp.MustCompile(`^(\d+\.\d+(?:\.\d+)?)-(\d+)$`)
)

// ValidationError collects every problem found, so one run reports them all
// and the user does not have to fix and rerun once per mistake.
//
// ValidationError - 발견된 모든 문제를 모은다. 한 번 실행에 다 리포트해서,
// 사용자가 실수마다 고치고 재실행하지 않아도 되게 한다.
type ValidationError struct {
	Problems []string
}

func (e *ValidationError) Error() string {
	if len(e.Problems) == 1 {
		return "config invalid: " + e.Problems[0]
	}
	return fmt.Sprintf("config invalid (%d problems):\n  - %s",
		len(e.Problems), strings.Join(e.Problems, "\n  - "))
}

// Validate checks the config against the catalog and returns a
// *ValidationError listing every problem.
//
// Validate - config 를 카탈로그에 대조 검증한다. 모든 문제를 나열한
// *ValidationError 를 반환한다.
func (c *Config) Validate(cat *catalog.Catalog) error {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, fmt.Sprintf(format, args...))
	}

	if c.Version != SchemaVersion {
		add("version is %d but this build of vibeldr speaks schema %d", c.Version, SchemaVersion)
	}

	// Model and platform. / 모델과 플랫폼.
	var plat *catalog.Platform
	if c.Model == "" {
		add("model is required (try `vibeldr models`)")
	} else {
		p, ok := cat.PlatformForModel(c.Model)
		if !ok {
			add("unknown model %q (try `vibeldr models`)", c.Model)
		} else {
			plat = p
		}
	}

	// DSM version, and whether the platform supports it.
	// DSM 버전과, 그 플랫폼이 지원하는지 여부.
	productVer := ""
	if c.DSM.Version == "" {
		add("dsm.version is required, e.g. \"7.4.1-90080\"")
	} else {
		m := dsmVersionPattern.FindStringSubmatch(c.DSM.Version)
		if m == nil {
			add("dsm.version %q must look like \"7.4.1-90080\"", c.DSM.Version)
		} else {
			parts := strings.Split(m[1], ".")
			productVer = parts[0] + "." + parts[1]
			// One DSM version is supported; the catalog also lists older ones.
			// 지원하는 DSM 버전은 하나다. 카탈로그에는 옛 버전도 올라 있다.
			if productVer != catalog.TargetProductVersion {
				add("dsm.version %q: only DSM %s is supported", c.DSM.Version, catalog.TargetProductVersion)
			} else if plat != nil {
				if _, ok := plat.Kernels[productVer]; !ok {
					add("platform %s does not support DSM %s (supported: %s)",
						plat.Name, productVer, strings.Join(cat.ProductVersions(plat.Name), ", "))
				}
			}
		}
	}

	if c.DSM.MD5 != "" && len(c.DSM.MD5) != 32 {
		add("dsm.md5 must be 32 hex characters (or empty to skip the check)")
	}

	// Identity. / 정체성.
	if c.Identity.Serial != "" && c.Model != "" {
		if err := cat.ValidateSerial(c.Model, c.Identity.Serial); err != nil {
			add("identity.serial: %v", err)
		}
	}
	if n := len(c.Identity.MACs); n > 8 {
		add("identity.macs has %d entries, the kernel cmdline supports at most 8", n)
	}
	for i, mac := range c.Identity.MACs {
		if _, err := catalog.NormalizeMAC(mac); err != nil {
			add("identity.macs[%d]: %v", i, err)
		}
	}
	if c.Identity.NICCount < 1 || c.Identity.NICCount > 8 {
		add("identity.nic_count must be between 1 and 8, got %d", c.Identity.NICCount)
	}
	if len(c.Identity.MACs) > 0 && len(c.Identity.MACs) != c.Identity.NICCount {
		add("identity.nic_count is %d but %d MAC(s) are listed; the two must agree",
			c.Identity.NICCount, len(c.Identity.MACs))
	}

	// Storage: portmaps only mean something on non-DT platforms.
	// 스토리지: 포트맵은 비-DT 플랫폼에서만 의미가 있다.
	if plat != nil && plat.DT {
		if c.Storage.SataPortMap != "" || c.Storage.DiskIdxMap != "" {
			add("platform %s uses a device tree, so storage.sata_portmap/disk_idx_map are ignored - remove them to avoid confusion", plat.Name)
		}
	}
	if (c.Storage.SataPortMap == "") != (c.Storage.DiskIdxMap == "") {
		add("storage.sata_portmap and storage.disk_idx_map must be set together")
	}

	// Storage: the bay override. 0 means "keep the default", so the only
	// lower bound checked is that it is not negative.
	//
	// 스토리지: 베이 override. 0 은 "기본 유지" 를 뜻하므로 하한이 음수가 아닌
	// 것만 본다.
	if c.Storage.Bays < 0 || c.Storage.Bays > 64 {
		add("storage.bays must be between 0 and 64, got %d", c.Storage.Bays)
	}

	// Free-form maps must not smuggle a space into the kernel cmdline.
	// 자유 형식 맵이 커널 커맨드라인에 공백을 몰래 넣지 못하게 한다.
	for k, v := range c.Cmdline {
		if k == "" {
			add("cmdline has an empty key")
		}
		if strings.ContainsAny(k, " \t") || strings.ContainsAny(v, " \t") {
			add("cmdline entry %q contains whitespace, which would split into two kernel parameters", k)
		}
	}
	for _, m := range c.Modules.Blacklist {
		if strings.ContainsAny(m, " \t,") {
			add("modules.blacklist entry %q must be a bare module name", m)
		}
	}

	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

// ProductVersion returns the "7.4" part of dsm.version.
// ProductVersion - dsm.version 에서 "7.4" 부분을 돌려준다.
func (c *Config) ProductVersion() string {
	m := dsmVersionPattern.FindStringSubmatch(c.DSM.Version)
	if m == nil {
		return ""
	}
	parts := strings.Split(m[1], ".")
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "." + parts[1]
}

// BuildNumber returns the "90080" part of dsm.version.
// BuildNumber - dsm.version 에서 "90080" 부분을 돌려준다.
func (c *Config) BuildNumber() string {
	m := dsmVersionPattern.FindStringSubmatch(c.DSM.Version)
	if m == nil {
		return ""
	}
	return m[2]
}
