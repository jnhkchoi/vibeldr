package main

import (
	"os"
	"path/filepath"
	"strings"
)

// On some mini NAS boxes - the UGREEN DXP2800 among them - one press of the
// power button fires two ACPI events, and DSM's shutdown flow is triggered
// twice. The reports say the second call lands in the middle of the shutdown
// sequence and the machine never reaches the reboot.
//
// The user-space answer: overwrite /etc/acpi/events/powerbtn with our own copy,
// whose action script debounces - anything within N seconds of the last event
// is ignored. The original is kept as .vibeldr.bak so it can be put back.
//
// The vendor and model are detected through DMI and this is applied only on
// those machines. Vendors other than UGREEN with a similar problem are in the
// detection list too, so nothing has to be configured by hand.
//
// UGREEN DXP2800 등 일부 미니 NAS 는 power button 을 한 번 눌러도 ACPI
// 이벤트가 두 번 발화되어, DSM 의 shutdown 흐름이 두 번 트리거된다.
// 결과적으로 종료 시퀀스 중간에 재-호출이 겹쳐 즉시 재부팅으로 이어지지
// 못하는 사례가 보고됐다.
//
// 유저스페이스 해법: /etc/acpi/events/powerbtn 을 우리 사본으로 덮어써서,
// 액션 스크립트에 "마지막 이벤트 이후 N 초 이내면 무시" 디바운스를 넣는다.
// 원본은 .vibeldr.bak 로 보존해서 사용자가 되돌릴 수 있게 한다.
//
// DMI 로 벤더/모델을 감지해 해당 머신에서만 적용한다. UGREEN 뿐 아니라
// 유사 이슈가 있는 다른 mini-NAS 벤더도 감지 대상에 넣어, 사용자가 별도
// 설정 없이도 커버되게 한다.

// acpidEventsDir is where acpid reads its event definition files.
// acpidEventsDir - acpid 가 이벤트 정의 파일을 읽는 표준 위치.
const acpidEventsDir = "/etc/acpi/events"

// powerbtnFile is the power button's event definition file.
// powerbtnFile - 전원 버튼 이벤트 정의 파일.
const powerbtnFile = "powerbtn"

// acpidActionScript is the action put in place of the original. Keeping the
// debounce logic in a script of its own leaves the event file as a plain
// mapping.
//
// acpidActionScript - 우리가 대신 넣는 액션. debounce 로직을 별도 스크립트로
// 분리해서 이벤트 파일은 단순한 매핑만 유지한다.
const acpidActionScript = "/etc/acpi/vibeldr-powerbtn.sh"

// acpidStateFile records when a power event was last consumed. Anything within
// three seconds is ignored. It is under /run, so a reboot clears it by itself.
//
// acpidStateFile - 마지막으로 전원 이벤트를 소비한 시각을 기록한다. 3 초 이내
// 재발화는 무시한다. /run 밑이라 재부팅되면 자연히 초기화된다.
const acpidStateFile = "/run/vibeldr/acpid-powerbtn.last"

// vibeldrEventBody is the body of the acpid event file. The event regexp is
// left in the original's form (^button/power); only the action points at our
// script.
//
// vibeldrEventBody - acpid 이벤트 파일 본문. event 정규식은 원본과 같은
// 형태 (^button/power) 를 그대로 쓰고, action 만 우리 스크립트로 바꾼다.
const vibeldrEventBody = `# vibeldr: replaces stock powerbtn action to debounce duplicate ACPI events
event=button/power.*
action=/etc/acpi/vibeldr-powerbtn.sh %e
`

// vibeldrActionBody is the shell script that does the debouncing. It leaves the
// time of the last handled event in a file under /run and exits quietly on any
// call within three seconds. Otherwise it shuts down with /sbin/shutdown -h now,
// or /sbin/poweroff when there is no shutdown.
//
// vibeldrActionBody - 실제 debounce 하는 셸 스크립트. 마지막 처리 시각을
// /run 밑 파일에 남기고, 3 초 이내 재호출은 조용히 끝낸다. 아니면
// /sbin/shutdown -h now 로 끄고, shutdown 이 없으면 /sbin/poweroff 를 쓴다.
const vibeldrActionBody = `#!/bin/sh
# vibeldr: powerbtn debounce
# UGREEN 등 일부 하드웨어에서 한 번 누른 전원 버튼이 두 번의 ACPI 이벤트로
# 오는 문제를 여기서 흡수. 마지막 소비 시각으로부터 3 초 이내 재-호출은
# 무시하고, 유효 호출이면 실제 종료를 진행.
set -eu
STATE=/run/vibeldr/acpid-powerbtn.last
NOW=$(date +%s)
mkdir -p "$(dirname "$STATE")"
if [ -r "$STATE" ]; then
    LAST=$(cat "$STATE" 2>/dev/null || echo 0)
    DIFF=$((NOW - LAST))
    if [ "$DIFF" -lt 3 ]; then
        exit 0
    fi
fi
echo "$NOW" > "$STATE"
# DSM 의 종료. shutdown/poweroff 어느 쪽이든 있는 것을 사용.
if [ -x /sbin/shutdown ]; then
    /sbin/shutdown -h now
elif [ -x /sbin/poweroff ]; then
    /sbin/poweroff
fi
`

// debounceVendorMarkers are the substrings that mark a target machine. A match
// in any of the DMI board_vendor, board_name, sys_vendor or product_name
// strings makes it one. Compared in lower case.
//
// debounceVendorMarkers - 대상 머신을 가리키는 부분 문자열. DMI 의
// board_vendor / board_name / sys_vendor / product_name 중 하나라도
// 매칭되면 대상 머신이다. 소문자로 비교한다.
var debounceVendorMarkers = []string{
	"ugreen",      // UGREEN DXP2800, DXP4800 and the rest of the DXP series / UGREEN DXP 시리즈 전반
	"dxp",         // UGREEN's DXP branding when it shows only in board_name / UGREEN 의 DXP 브랜딩이 board_name 에만 나오는 경우
	"terramaster", // TerraMaster F series / TerraMaster F 시리즈
	"zimaboard",   // ZimaBoard / ZimaBlade
	"zimacube",    // ZimaCube
}

// patchACPI detects the target hardware through DMI and, where it matches,
// overwrites the acpid event file and the action script. It returns how many
// files were actually touched.
//
// patchACPI - DMI 로 대상 하드웨어를 감지해, 감지되면 acpid 이벤트 파일과
// 액션 스크립트를 덮어쓴다. 반환값은 실제로 손댄 파일 개수.
func patchACPI() int {
	if !isDebounceCandidate() {
		return 0
	}
	vendor := readTrimmed("/sys/class/dmi/id/board_vendor")
	board := readTrimmed("/sys/class/dmi/id/board_name")
	logf("acpid: known hardware (vendor=%q board=%q); applying power button debounce", vendor, board)

	var n int
	if writeACPIEvent() {
		n++
	}
	if writeACPIAction() {
		n++
	}
	return n
}

// isDebounceCandidate is true when any vendor marker appears in the DMI strings.
// isDebounceCandidate - DMI 문자열에 하나라도 벤더 마커가 있으면 true.
func isDebounceCandidate() bool {
	fields := []string{
		"/sys/class/dmi/id/board_vendor",
		"/sys/class/dmi/id/board_name",
		"/sys/class/dmi/id/sys_vendor",
		"/sys/class/dmi/id/product_name",
	}
	for _, f := range fields {
		v := strings.ToLower(readTrimmed(f))
		if v == "" {
			continue
		}
		for _, m := range debounceVendorMarkers {
			if strings.Contains(v, m) {
				return true
			}
		}
	}
	return false
}

// writeACPIEvent replaces the powerbtn event file with our copy, keeping the
// original as .vibeldr.bak. A file that already carries our marker is skipped.
//
// writeACPIEvent - powerbtn 이벤트 파일을 우리 사본으로 교체한다.
// 원본은 .vibeldr.bak 로 보존한다. 이미 우리 마커가 있으면 건너뛴다.
func writeACPIEvent() bool {
	path := filepath.Join(acpidEventsDir, powerbtnFile)
	if existing, err := os.ReadFile(path); err == nil {
		if strings.Contains(string(existing), "vibeldr:") {
			return false // already ours / 이미 우리 것
		}
		bak := path + ".vibeldr.bak"
		if _, err := os.Stat(bak); os.IsNotExist(err) {
			// With no backup yet, keep a copy of the original.
			// 백업이 아직 없으면 원본을 복사해 둔다.
			_ = os.WriteFile(bak, existing, 0o644)
		}
	} else if !os.IsNotExist(err) {
		logf("acpid: %s: %v", path, err)
		return false
	}
	if err := os.MkdirAll(acpidEventsDir, 0o755); err != nil {
		logf("acpid: %s: %v", acpidEventsDir, err)
		return false
	}
	if err := os.WriteFile(path, []byte(vibeldrEventBody), 0o644); err != nil {
		logf("acpid: %s: %v", path, err)
		return false
	}
	return true
}

// writeACPIAction puts the action script in place. The executable bit is
// required.
//
// writeACPIAction - 액션 스크립트를 심는다. 실행 비트가 필수다.
func writeACPIAction() bool {
	if existing, err := os.ReadFile(acpidActionScript); err == nil {
		if string(existing) == vibeldrActionBody {
			return false // same content already / 이미 같은 내용
		}
	}
	dir := filepath.Dir(acpidActionScript)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logf("acpid: %s: %v", dir, err)
		return false
	}
	if err := os.WriteFile(acpidActionScript, []byte(vibeldrActionBody), 0o755); err != nil {
		logf("acpid: %s: %v", acpidActionScript, err)
		return false
	}
	// Create the state directory up front too, so the script can write safely.
	// state 디렉터리도 미리 만들어 둔다 (스크립트가 안전하게 쓸 수 있게).
	_ = os.MkdirAll(filepath.Dir(acpidStateFile), 0o755)
	return true
}
