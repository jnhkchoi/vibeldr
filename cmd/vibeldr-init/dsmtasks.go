package main

import (
	"strings"

	"vibeldr/internal/synoboot"
)

// Repairs to the installed DSM, asked for in the loader's rescue menu and made
// on DSM's next boot.
//
// The loader's own kernel cannot write DSM's system partition safely: it is a
// RAID 1 across every disk, and DSM keeps its RAID superblocks big-endian,
// which a stock kernel does not assemble. Writing one member alone would leave
// the mirrors disagreeing. So the loader only writes synoboot.DSMTasksFile, and
// the helper makes the repairs at -detach, where DSM's kernel has assembled the
// array and mounted it at newRoot (dsmtasks_linux.go).
//
// 로더 복구 메뉴에서 요청하고 DSM 의 다음 부팅 때 하는, 설치된 DSM 의 수리.
//
// 로더 자신의 커널은 DSM 시스템 파티션에 안전하게 쓸 수 없다. 그것은 모든
// 디스크에 걸친 RAID 1 이고, DSM 은 RAID 슈퍼블록을 빅엔디언으로 두어 일반
// 커널은 조립하지 못한다. 멤버 하나에만 쓰면 미러가 서로 어긋난다. 그래서
// 로더는 synoboot.DSMTasksFile 만 쓰고, 헬퍼가 -detach 에서 수리한다. 그때는
// DSM 커널이 어레이를 조립해 newRoot 에 붙여 두었다 (dsmtasks_linux.go).

// dsmTask is one repair: an SQL statement run by DSM's own sqlite3 on one of
// its databases, given relative to the DSM root.
//
// dsmTask - 수리 하나. DSM 자신의 sqlite3 로 DSM 루트 기준 경로의 DB 하나에
// 돌리는 SQL 문이다.
type dsmTask struct {
	db  string
	sql string
}

// dsmTasks are the repairs by the name synoboot.DSMTasksFile uses.
//
// The auto-block list keeps its allow entries (Deny=0), which deleting the
// whole file would lose. Triggered tasks are the boot-up and shutdown scripts
// of Task Scheduler; time-based schedules live elsewhere and are left alone.
//
// dsmTasks - synoboot.DSMTasksFile 이 쓰는 이름별 수리.
//
// 자동 차단 목록에서 허용 항목(Deny=0)은 남긴다. 파일을 통째로 지우면 그것까지
// 잃는다. 트리거 작업은 작업 스케줄러의 부팅·종료 스크립트다. 시간 예약 작업은
// 다른 곳에 있어 건드리지 않는다.
var dsmTasks = map[string]dsmTask{
	synoboot.TaskUnblockIPs:       {"/etc/synoautoblock.db", "DELETE FROM AutoBlockIP WHERE Deny=1;"},
	synoboot.TaskDisableTriggered: {"/usr/syno/etc/esynoscheduler/esynoscheduler.db", "UPDATE task SET enable=0;"},
}

// parseDSMTasks reads the file's lines into known task names, in order and
// without repeats, and the unknown ones separately.
//
// parseDSMTasks - 파일의 줄을 아는 작업 이름(순서대로, 중복 없이)과 모르는
// 이름으로 나눈다.
func parseDSMTasks(data string) (known, unknown []string) {
	seen := map[string]bool{}
	for _, line := range strings.Split(data, "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasPrefix(name, "#") || seen[name] {
			continue
		}
		seen[name] = true
		if _, ok := dsmTasks[name]; ok {
			known = append(known, name)
		} else {
			unknown = append(unknown, name)
		}
	}
	return known, unknown
}
