//go:build linux

package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// A snapshot of how DSM sees the machine, taken by the agent once DSM is up.
//
// Without a shell into DSM, and DSM keeps none by default, what it made of the
// disks, the network cards and the M.2 slots can only be guessed at from its
// logs. The agent runs a few read-only commands and writes their output to
// stateLog, which the loader's diagnostics bundle picks up with the rest of
// /var/log (cmd/vibeldr-boot/diag_linux.go).
//
// DSM 이 올라온 뒤 에이전트가 찍는, DSM 이 이 기계를 어떻게 보는지의 스냅샷.
//
// DSM 에 셸이 없으면 (기본으로는 없다) DSM 이 디스크, 랜카드, M.2 슬롯을 어떻게
// 받아들였는지 로그에서 짐작할 수밖에 없다. 에이전트가 읽기만 하는 명령 몇 개를
// 돌려 결과를 stateLog 에 쓰고, 로더의 진단 묶음이 /var/log 의 나머지와 함께
// 가져간다 (cmd/vibeldr-boot/diag_linux.go).

// stateLog is where the snapshot goes; it is replaced on every boot.
// stateLog - 스냅샷이 가는 곳. 부팅마다 새로 쓴다.
const stateLog = "/var/log/vibeldr-state.log"

// stateCommands are the commands, all read-only.
// stateCommands - 명령들. 모두 읽기만 한다.
var stateCommands = [][]string{
	{"/bin/uname", "-a"},
	{"/sbin/ip", "-4", "addr"},
	{"/bin/cat", "/proc/mdstat"},
	{"/usr/syno/bin/synodisk", "--enum"},
	{"/sbin/lsmod"},
	{"/bin/grep", "-E", "^(support_syno_hybrid_raid|supportraidgroup|supportnvme|support_m2_pool|maxlanport|maxdisks)=", "/etc/synoinfo.conf"},
}

// writeStateLog runs stateCommands, and synonvme --get-location for every NVMe
// disk, into stateLog.
//
// writeStateLog - stateCommands 와, NVMe 디스크마다 synonvme --get-location 을
// 돌려 stateLog 에 쓴다.
func writeStateLog() {
	cmds := append([][]string(nil), stateCommands...)
	nvme, _ := filepath.Glob("/dev/nvme[0-9]n[0-9]")
	for _, d := range nvme {
		cmds = append(cmds, []string{"/usr/syno/bin/synonvme", "--get-location", d})
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "vibeldr state %s\n", time.Now().Format(time.RFC3339))
	for _, c := range cmds {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		out, err := exec.CommandContext(ctx, c[0], c[1:]...).CombinedOutput()
		cancel()
		fmt.Fprintf(&b, "\n$ %v\n%s", c, out)
		if err != nil {
			fmt.Fprintf(&b, "(%v)\n", err)
		}
	}
	if err := os.WriteFile(stateLog, b.Bytes(), 0o644); err != nil {
		logf("agent: %s: %v", stateLog, err)
	}
}
