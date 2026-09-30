//go:build linux

package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"vibeldr/internal/hwscan"
	"vibeldr/internal/kmod"
	"vibeldr/internal/synoboot"
)

const (
	// dsmTasksMount is where the loader's partition 1 is mounted to read
	// synoboot.DSMTasksFile.
	//
	// dsmTasksMount - synoboot.DSMTasksFile 을 읽으려고 로더 파티션 1 을 붙이는
	// 자리.
	dsmTasksMount = "/tmp/vibeldr-tasks-p1"
	// dsmTasksLog is the record of the repairs, inside DSM.
	// dsmTasksLog - DSM 안에 남는 수리 기록.
	dsmTasksLog = "/var/log/vibeldr-tasks.log"
	// dsmTasksBackup is added to a database's name for the copy kept before
	// changing it.
	//
	// dsmTasksBackup - DB 를 바꾸기 전에 남기는 사본의 이름 뒤에 붙인다.
	dsmTasksBackup = ".vibeldr-bak"
)

// runDSMTasks makes the repairs synoboot.DSMTasksFile asks for on the DSM
// mounted at root. The file is removed before anything is changed, so a repair
// that goes wrong is not tried again on every boot.
//
// runDSMTasks - synoboot.DSMTasksFile 이 요청한 수리를 root 에 붙은 DSM 에
// 한다. 파일은 무엇이든 바꾸기 전에 지운다. 그래야 잘못되는 수리가 부팅마다
// 다시 시도되지 않는다.
func runDSMTasks(root string) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "vibeldr repairs %s\n", time.Now().Format(time.RFC3339))
	if n, err := doDSMTasks(root, &b); err != nil {
		fmt.Fprintf(&b, "error: %v\n", err)
	} else if n == 0 {
		// Nothing was queued: no record.
		// 예약된 것이 없다. 기록하지 않는다.
		return
	}
	for _, line := range strings.Split(strings.TrimSpace(b.String()), "\n") {
		logf("dsm tasks: %s", line)
	}
	// Into DSM as well: on a machine whose DSM kernel writes nothing to the
	// serial port, this file is the only place to read what happened.
	//
	// DSM 안에도 남긴다. DSM 커널이 시리얼에 아무것도 쓰지 않는 기계에서는 무슨
	// 일이 있었는지 읽을 곳이 이 파일뿐이다.
	logPath := filepath.Join(root, dsmTasksLog)
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		f.Write(b.Bytes())
		f.Close()
	}
}

// doDSMTasks takes the queued repairs and makes them, writing one line per
// name to b, and says how many names there were. An error means the queue
// could not be read at all.
//
// doDSMTasks - 예약된 수리를 가져와 하고, 이름마다 한 줄을 b 에 쓰며, 이름이
// 몇 개였는지 돌려준다. 오류는 예약 자체를 읽지 못했다는 뜻이다.
func doDSMTasks(root string, b *bytes.Buffer) (int, error) {
	node, err := loaderPartition1()
	if err != nil {
		return 0, err
	}
	data, err := takeDSMTasks(node)
	os.Remove(node)
	if err != nil || data == "" {
		return 0, err
	}
	known, unknown := parseDSMTasks(data)
	for _, name := range unknown {
		fmt.Fprintf(b, "%s: unknown, skipped\n", name)
	}
	for _, name := range known {
		fmt.Fprintf(b, "%s: %s\n", name, applyDSMTask(root, dsmTasks[name]))
	}
	return len(known) + len(unknown), nil
}

// loaderPartition1 makes a node for the loader's partition 1 (loaderP1Node,
// which the caller removes). init.post mounts /dev again just before -detach,
// so a node made earlier is not trusted: the disk is found by its label, as
// flushLog does, and the node is made from its numbers in sysfs. The ramdisk
// has unmounted /sys by then, so it is mounted for the lookup and taken down
// again.
//
// loaderPartition1 - 로더 파티션 1 의 노드를 만든다 (loaderP1Node, 호출자가
// 지운다). init.post 가 -detach 바로 전에 /dev 를 다시 붙이므로 앞서 만든
// 노드는 믿지 않는다. flushLog 처럼 라벨로 디스크를 찾고 sysfs 의 번호로 노드를
// 만든다. 그때쯤 램디스크가 /sys 를 떼어 두었으므로, 찾는 동안만 붙였다가 다시
// 뗀다.
func loaderPartition1() (string, error) {
	if _, err := os.Stat(synoboot.DefaultSysBlock); os.IsNotExist(err) {
		if err := syscall.Mount("sysfs", "/sys", "sysfs", 0, ""); err != nil {
			return "", fmt.Errorf("mount /sys: %w", err)
		}
		defer syscall.Unmount("/sys", 0)
	}
	disk, err := synoboot.FindDisk(synoboot.DefaultSysBlock, hwscan.DefaultDev, loaderLabel())
	if err != nil {
		return "", err
	}
	return partitionNodeOf(disk, 1, loaderP1Node)
}

// fatModules are what mounting the loader's FAT partition needs. On a normal
// boot DSM loads fat and vfat only later, from /etc/rc, so they may not be in
// yet at -detach.
//
// fatModules - 로더의 FAT 파티션을 붙이는 데 필요한 것. 정상 부팅에서 DSM 은
// fat 과 vfat 을 나중에 /etc/rc 에서야 올리므로 -detach 때는 아직 없을 수 있다.
var fatModules = []string{"fat", "vfat", "nls_cp437", "nls_iso8859_1", "nls_utf8"}

// loadFAT loads fatModules from the ramdisk; ones already in are skipped by
// the kernel.
//
// loadFAT - 램디스크에서 fatModules 를 올린다. 이미 올라온 것은 커널이 건너뛴다.
func loadFAT() {
	index, err := kmod.Scan(kmod.SearchDirs...)
	if err != nil {
		return
	}
	for _, m := range index.ByName(fatModules...) {
		_ = kmod.Load(m)
	}
}

// mountDiag says, for a failed mount, which device the node points at and
// whether the kernel knows vfat.
//
// mountDiag - 실패한 마운트에 대해, 노드가 가리키는 장치와 커널이 vfat 을
// 아는지를 말한다.
func mountDiag(node string) string {
	dev := "?"
	var st syscall.Stat_t
	if syscall.Stat(node, &st) == nil {
		dev = fmt.Sprintf("%d:%d", (st.Rdev>>8)&0xfff, (st.Rdev&0xff)|((st.Rdev>>12)&0xfff00))
	}
	fs, _ := os.ReadFile("/proc/filesystems")
	return fmt.Sprintf("node %s, vfat known %v", dev, strings.Contains(string(fs), "vfat"))
}

// takeDSMTasks reads synoboot.DSMTasksFile off the loader partition at node
// and removes it. No file gives "" and no error.
//
// takeDSMTasks - node 의 로더 파티션에서 synoboot.DSMTasksFile 을 읽고 지운다.
// 파일이 없으면 "" 이고 오류는 없다.
func takeDSMTasks(node string) (string, error) {
	if err := os.MkdirAll(dsmTasksMount, 0o755); err != nil {
		return "", err
	}
	defer os.Remove(dsmTasksMount)
	loadFAT()
	if err := syscall.Mount(node, dsmTasksMount, "vfat", 0, ""); err != nil {
		return "", fmt.Errorf("mount %s: %w (%s)", node, err, mountDiag(node))
	}
	defer syscall.Unmount(dsmTasksMount, 0)
	path := filepath.Join(dsmTasksMount, synoboot.DSMTasksFile)
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("remove %s: %w", synoboot.DSMTasksFile, err)
	}
	syscall.Sync()
	return string(raw), nil
}

// applyDSMTask copies the database aside and runs the statement with DSM's own
// sqlite3, chrooted into root so that it finds its libraries. It says what
// happened, in words for the log.
//
// applyDSMTask - DB 사본을 남기고, DSM 자신의 sqlite3 를 root 로 chroot 해
// (라이브러리를 찾도록) 문을 돌린다. 무슨 일이 있었는지 로그용 문장으로 돌려준다.
func applyDSMTask(root string, t dsmTask) string {
	db := filepath.Join(root, t.db)
	if _, err := os.Stat(db); err != nil {
		return "no " + t.db + ", nothing to do"
	}
	if err := copyFile(db, db+dsmTasksBackup); err != nil {
		return fmt.Sprintf("not changed: backup: %v", err)
	}
	cmd := exec.Command("/usr/bin/sqlite3", t.db, t.sql+" SELECT changes();")
	cmd.SysProcAttr = &syscall.SysProcAttr{Chroot: root}
	cmd.Dir = "/"
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Sprintf("failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return fmt.Sprintf("done, %s rows changed (copy kept as %s%s)", strings.TrimSpace(string(out)), t.db, dsmTasksBackup)
}

// copyFile copies src to dst with src's permissions.
// copyFile - src 를 그 권한 그대로 dst 에 복사한다.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
