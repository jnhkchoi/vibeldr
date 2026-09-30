//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"vibeldr/internal/synoboot"
)

// Repairs to the installed DSM, queued here and made on DSM's next boot.
//
// This environment cannot write DSM's system partition safely (it is a RAID 1
// whose superblocks this kernel does not read), so the choice is written to
// synoboot.DSMTasksFile on partition 1 and the helper inside DSM's boot does
// the work (cmd/vibeldr-init/dsmtasks.go). What happened is logged in DSM's
// /var/log/vibeldr-tasks.log.
//
// 여기서 예약하고 DSM 의 다음 부팅 때 하는, 설치된 DSM 의 수리.
//
// 이 환경은 DSM 시스템 파티션에 안전하게 쓸 수 없다 (이 커널이 슈퍼블록을 읽지
// 못하는 RAID 1 이다). 그래서 고른 것을 파티션 1 의 synoboot.DSMTasksFile 에
// 적고, DSM 부팅 안의 헬퍼가 일을 한다 (cmd/vibeldr-init/dsmtasks.go). 결과는
// DSM 의 /var/log/vibeldr-tasks.log 에 남는다.

// dsmFixChoices are the repairs offered, with what each says on the console.
// dsmFixChoices - 내놓는 수리와 콘솔에 보일 설명.
var dsmFixChoices = []struct {
	task, label string
}{
	{synoboot.TaskUnblockIPs, "Clear the IP auto-block list (the allow list stays)"},
	{synoboot.TaskDisableTriggered, "Turn off all triggered tasks (boot-up / shutdown scripts)"},
}

// dsmFixMenu asks for each repair in turn and queues the ones chosen,
// replacing whatever was queued before. Choosing none clears the queue.
//
// dsmFixMenu - 수리마다 차례로 묻고 고른 것을 예약한다. 전에 예약한 것은
// 바꾼다. 아무것도 고르지 않으면 예약을 지운다.
func dsmFixMenu() {
	screenClear()
	fmt.Println("Repair DSM on next boot")
	fmt.Println("-----------------------")
	fmt.Println("The repairs run when DSM next starts, and the result goes to")
	fmt.Println("/var/log/vibeldr-tasks.log inside DSM. Each database is copied")
	fmt.Println("to <name>.vibeldr-bak before it is changed.")
	fmt.Println()
	path := filepath.Join(loaderMount, synoboot.DSMTasksFile)
	queued, _ := os.ReadFile(path)
	var chosen []string
	for _, c := range dsmFixChoices {
		def := "n"
		if strings.Contains(string(queued), c.task) {
			def = "y"
		}
		if ans := strings.ToLower(prompt(fmt.Sprintf("%s? [y/n, now %s]: ", c.label, def))); ans == "y" || (ans == "" && def == "y") {
			chosen = append(chosen, c.task)
		}
	}
	if err := saveDSMFix(path, chosen); err != nil {
		fmt.Println("could not save:", err)
	} else if len(chosen) == 0 {
		fmt.Println("nothing queued")
	} else {
		fmt.Println("queued: " + strings.Join(chosen, ", ") + ". Reboot into DSM to run them.")
	}
	pause("")
}

// saveDSMFix writes the chosen tasks to path, or removes it when there are
// none.
//
// saveDSMFix - 고른 작업을 path 에 쓴다. 없으면 파일을 지운다.
func saveDSMFix(path string, tasks []string) error {
	if len(tasks) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	body := "# queued in the vibeldr rescue menu; run and removed on DSM's next boot\n" +
		strings.Join(tasks, "\n") + "\n"
	return os.WriteFile(path, []byte(body), 0o644)
}
