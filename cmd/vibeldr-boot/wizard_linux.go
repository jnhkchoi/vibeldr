//go:build linux

// wizard_linux.go is the install wizard drawn on the framebuffer.
//
// With a screen this wizard comes up; without one the boot goes to the text
// menu. Everything the user chooses lands in loader.yaml (config.Config), and
// the last step runs the very same build pipeline as the text menu. Both
// screens use the same settings and the same pipeline, so installing from
// either gives the same result.
//
// The steps are walked once each, top to bottom:
//
//	start -> model -> boot disk -> storage -> network -> serial -> confirm -> install
//
// wizard_linux.go - 프레임버퍼 위에 그리는 설치 마법사.
//
// 화면이 있으면 이 마법사가 뜨고, 없으면 글자 메뉴로 넘어간다. 사용자가
// 고른 값은 전부 loader.yaml (config.Config) 에 들어가고, 마지막 단계에서
// 글자 메뉴와 똑같은 빌드 파이프라인을 돌린다. 두 화면이 같은 설정과 같은
// 파이프라인을 쓰므로 어느 쪽으로 설치해도 결과가 같다.
//
// 단계는 위에서 아래로 한 번씩 밟는다:
//
//	시작 -> 모델 -> 부트 디스크 -> 저장소 -> 네트워크 -> 시리얼 -> 확인 -> 설치
package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
	"sync"

	"vibeldr/internal/catalog"
	"vibeldr/internal/fbui"
	"vibeldr/internal/hwscan"
	"vibeldr/internal/ui"
)

// wizardState is the state held while the wizard runs.
// wizardState - 마법사가 도는 동안의 상태.
type wizardState struct {
	st   *tuiState
	sess *fbui.Session
	wiz  *fbui.Wizard

	// controllers are the disk controllers read from sysfs at boot.
	// controllers - 부팅 시점에 sysfs 에서 읽은 디스크 컨트롤러.
	controllers []hwscan.Controller
	// embedded are the models whose decrypted kernel is on the image's P2.
	// Installing one of these downloads nothing and decrypts nothing.
	//
	// embedded - 이미지 P2 에 복호화된 커널이 들어있는 모델. 여기 있는
	// 모델은 설치 때 다운로드도 복호화도 하지 않는다.
	embedded map[string]bool

	// nics are the network interfaces that were detected.
	// nics - 감지된 네트워크 인터페이스.
	nics []nicInfo
	// hyp is which hypervisor this is on, or bare metal.
	// hyp - 어떤 하이퍼바이저 위인지 (또는 베어메탈).
	hyp hwscan.Hypervisor

	// storagePage is which page of the port list the storage step is showing,
	// counting from 0. A 12-bay machine has more ports than fit on one screen.
	// Changing a dropdown rebuilds the body, and keeping the page here stops
	// it jumping back to page 1 when that happens.
	//
	// storagePage - 저장소 단계에서 보고 있는 포트 목록의 페이지 (0 기반).
	// 12 베이 기계는 포트가 한 화면에 안 들어가서 나눠 보여준다. 드롭다운을
	// 하나 바꾸면 본문을 다시 짓는데, 그때 1 페이지로 튀지 않도록 여기 남긴다.
	storagePage int

	// macFields are the fields for typing MAC addresses in on the network
	// step. They are empty unless setting them by hand was chosen.
	//
	// macFields - 네트워크 단계에서 MAC 을 직접 넣는 칸. 직접 지정을
	// 고르지 않았으면 비어 있다.
	macFields []*fbui.TextField

	// serialManual says whether the serial number is typed in by hand.
	// serialManual - 시리얼을 손으로 넣을지.
	serialManual bool
	// serialText is the serial number being typed in.
	// serialText - 손으로 넣는 중인 시리얼.
	serialText string

	// log is the window that takes the pipeline's output on the install step.
	// log - 설치 단계에서 파이프라인 출력을 받는 창.
	log *fbui.LogView
	// bar and progress are the install step's overall progress bar and what
	// drives it.
	// bar, progress - 설치 단계의 전체 진행 막대와 그것을 움직이는 값.
	bar      *fbui.ProgressBar
	progress *installProgress
	// installOnce makes sure the install only ever starts once.
	// installOnce - 설치를 한 번만 시작하게.
	installOnce sync.Once
	// installDone says whether the pipeline has finished.
	// installDone - 파이프라인이 끝났는지.
	installDone bool
	// installFailed says whether the pipeline ended in failure.
	// installFailed - 파이프라인이 실패로 끝났는지.
	installFailed bool
	mu            sync.Mutex
}

// nicInfo is what is shown about one network interface.
// nicInfo - 네트워크 인터페이스 한 개의 표시용 정보.
type nicInfo struct {
	Name string
	MAC  string
	// Driver is the driver name sysfs reports, empty when it is not known.
	// Driver - sysfs 가 알려주는 드라이버 이름. 모르면 빈 문자열.
	Driver string
}

// runGUIWizard puts the framebuffer wizard up.
//
// If the screen will not open it returns false and the caller goes to the text
// menu. It returns when the wizard closes, whether the user ran it to the end
// or shut it part way.
//
// runGUIWizard - 프레임버퍼 마법사를 띄운다.
//
// 화면을 열지 못하면 false 를 돌려주고, 호출자는 글자 메뉴로 넘어간다.
// 사용자가 끝까지 진행했든 중간에 껐든 마법사가 닫히면 돌아온다.
func runGUIWizard(st *tuiState) bool {
	if !fbui.Available("") {
		say("framebuffer: /dev/fb0 이 없어 글자 메뉴로 진행")
		return false
	}
	sess, err := fbui.OpenSession("")
	if err != nil {
		say("framebuffer: %v - 글자 메뉴로 진행", err)
		return false
	}
	defer sess.Close()
	say("framebuffer: %s", sess.Info())

	// Nothing goes to the console directly while the screen is being drawn.
	// It is the same framebuffer, so the text would land on the picture.
	//
	// 화면을 그리는 동안에는 콘솔에 직접 찍지 않는다. 같은 프레임버퍼라
	// 글자가 그림 위에 얹힌다.
	setConsoleQuiet(true)
	defer setConsoleQuiet(false)

	ws := &wizardState{
		st: st, sess: sess,
		log: fbui.NewLogView(),
	}
	ws.scanHardware()

	c := sess.Canvas()
	th := fbui.DefaultTheme()
	// On a small screen the margins and the font shrink so the lists are not
	// cut off.
	//
	// 화면이 작으면 여백과 글꼴을 줄여 목록이 잘리지 않게 한다.
	if c.H < 700 {
		th.TitleH = 48
		th.ButtonBarH = 56
		th.RowH = 28
		th.Pad = 10
		th.SidebarW = 190
	}

	ws.wiz = fbui.NewWizard(c, th, "vibeldr", ws.steps())
	ws.wiz.Subtitle = "DSM 부트로더 설치"
	ws.wiz.OnFinish = func() { ws.wiz.Finish() }

	sess.Run(ws.wiz, ws.tick)
	return true
}

// scanHardware reads this machine's hardware before any screen is laid out.
// scanHardware - 화면을 꾸미기 전에 이 기계의 하드웨어를 읽어둔다.
func (ws *wizardState) scanHardware() {
	ws.hyp = hwscan.DetectHypervisorSysfs()
	if cs, err := hwscan.ScanSysfs(); err == nil {
		ws.controllers = hwscan.OrderForBays(cs)
	}
	ws.nics = scanNICs()
	ws.embedded = listEmbeddedDSM(ws.st)
}

// scanNICs reads the interfaces, their MACs and their drivers from
// /sys/class/net.
//
// scanNICs - /sys/class/net 에서 인터페이스와 MAC, 드라이버를 읽는다.
func scanNICs() []nicInfo {
	names, err := interfaces()
	if err != nil {
		return nil
	}
	out := make([]nicInfo, 0, len(names))
	for _, n := range names {
		info := nicInfo{Name: n}
		if b, err := os.ReadFile("/sys/class/net/" + n + "/address"); err == nil {
			info.MAC = strings.ToUpper(strings.TrimSpace(string(b)))
		}
		// The driver name is the last part of the device/driver symlink.
		// 드라이버 이름은 device/driver 심볼릭 링크의 마지막 조각.
		if p, err := os.Readlink("/sys/class/net/" + n + "/device/driver"); err == nil {
			info.Driver = p[strings.LastIndex(p, "/")+1:]
		}
		out = append(out, info)
	}
	return out
}

// tick is called periodically while there is no input. There is only something
// to do during an install.
//
// tick - 입력이 없는 동안 주기적으로 불린다. 설치 중에만 할 일이 있다.
func (ws *wizardState) tick() bool {
	if ws.wiz == nil || ws.wiz.Step() != stepInstall {
		return false
	}
	// The log keeps arriving, so the install screen is redrawn every time.
	// 로그가 계속 들어오므로 설치 화면은 매번 다시 그린다.
	ws.mu.Lock()
	done, failed := ws.installDone, ws.installFailed
	ws.mu.Unlock()
	if ws.bar != nil && ws.progress != nil {
		ws.bar.Value, ws.bar.Text = ws.progress.value()
		if done && failed {
			ws.bar.Text = "실패"
		}
	}
	if done {
		if failed {
			ws.wiz.SetStatus("설치가 완료되지 못했습니다. 아래 로그를 확인하세요.", true)
		} else {
			ws.wiz.SetStatus("설치 완료. 재부팅하면 DSM 으로 들어갑니다.", false)
		}
		ws.wiz.SetNextEnabled(true)
	}
	return true
}

// The step numbers, used for jumping to another step.
// 단계 번호. 다른 단계로 건너뛸 때 쓴다.
const (
	stepStart = iota
	stepModel
	stepBootDisk
	stepStorage
	stepNetwork
	stepIdentity
	stepConfirm
	stepInstall
)

// steps is the definition of the wizard's steps.
// steps - 마법사 단계 정의.
func (ws *wizardState) steps() []fbui.Step {
	return []fbui.Step{
		{
			Title:    "시작",
			Heading:  "vibeldr 설치 마법사",
			Subtitle: "시작하기 전에 확인하세요.",
			Build:    ws.buildStart,
		},
		{
			Title:    "모델",
			Heading:  "모델 선택",
			Subtitle: "이 컴퓨터를 어떤 시놀로지 모델로 인식시킬지 고릅니다.",
			Build:    ws.buildModel,
			Validate: ws.validateModel,
		},
		{
			Title:    "부트 디스크",
			Heading:  "부트로더 디스크",
			Subtitle: "로더가 들어있는 디스크입니다. DSM 은 이 디스크를 저장소로 쓰지 않습니다.",
			Build:    ws.buildBootDisk,
		},
		{
			Title:    "저장소",
			Heading:  "디스크를 베이에 배치",
			Subtitle: "감지된 포트를 몇 번 베이에 놓을지 정합니다.",
			Build:    ws.buildStorage,
			Validate: ws.validateStorage,
		},
		{
			Title:    "네트워크",
			Heading:  "랜카드와 MAC 주소",
			Subtitle: "감지된 랜카드 순서대로 DSM 의 eth 번호가 붙습니다.",
			Build:    ws.buildNetwork,
			Validate: ws.validateNetwork,
		},
		{
			Title:    "시리얼",
			Heading:  "시리얼 번호",
			Subtitle: "고른 모델의 규칙에 맞는 시리얼을 만들거나 직접 넣습니다.",
			Build:    ws.buildIdentity,
			Validate: ws.validateIdentity,
		},
		{
			Title:     "확인",
			Heading:   "설정 확인",
			Subtitle:  "아래 내용으로 로더를 만듭니다. 고치려면 뒤로 가세요.",
			Build:     ws.buildConfirm,
			NextLabel: "설치 시작",
		},
		{
			Title:     "설치",
			Heading:   "설치 중",
			Subtitle:  "완료될 때까지 전원을 끄지 마세요.",
			Build:     ws.buildInstall,
			HideBack:  true,
			NextLabel: "재부팅",
			Validate:  ws.validateInstall,
		},
	}
}

// buildStart says what is about to happen and shows what was found on this
// computer, naming each item.
//
// A count alone gives the user no way to tell whether "2" is right. Listing the
// controllers, the disks and the network cards by name means it can be judged
// from this screen whether everything plugged in was picked up.
//
// buildStart - 무엇을 할 것인지 알리고, 이 컴퓨터에서 무엇을 찾았는지
// 항목마다 이름까지 보여준다.
//
// 개수만 적으면 "2 개" 가 맞는지 사용자가 확인할 길이 없다. 컨트롤러와
// 디스크와 랜카드를 이름으로 늘어놓으면, 꽂은 것이 다 잡혔는지 이 화면
// 에서 바로 판단할 수 있다.
func (ws *wizardState) buildStart(w *fbui.Wizard, p *fbui.Panel, body fbui.Rect) {
	th := w.Theme()
	f := fbui.NewForm(body, th)

	for _, line := range []string{
		"이 마법사는 시놀로지 DSM 을 이 컴퓨터에서 돌리기 위한 부트로더를 설치합니다.",
		"모델을 고르면, 이 컴퓨터의 디스크와 랜카드를 그 모델에 맞게 배치하고",
		"DSM " + catalog.TargetProductVersion + " 커널을 이 컴퓨터에 맞게 고친 뒤 부트 디스크에 씁니다.",
	} {
		f.Text(line, false)
	}
	f.Space(th.Pad)

	rows := ws.detectedRows()
	card := fbui.NewCard("감지된 시스템", rows...)
	card.LabelW = 190
	f.Full(card, fbui.CardHeight(th, "감지된 시스템", len(rows)))

	f.Space(th.Pad)
	if disks, _ := ws.diskCounts(); disks == 0 {
		f.Text("저장소로 쓸 디스크가 없습니다. DSM 설치를 마치려면 디스크가 최소 한 대 필요합니다.", true)
	} else {
		f.Text("계속하려면 다음을 누르세요.", true)
	}
	p.Add(f.Widgets()...)
}

// detectedRows lays a detection result out as one count line followed by the
// names.
//
// detectedRows - 감지 결과를 "개수 한 줄 + 이름 여러 줄" 로 편다.
func (ws *wizardState) detectedRows() []fbui.CardRow {
	var rows []fbui.CardRow

	rows = append(rows, fbui.CardRow{
		Label: "디스크 컨트롤러",
		Value: fmt.Sprintf("%d 개", len(ws.controllers)),
	})
	for _, c := range ws.controllers {
		rows = append(rows, fbui.CardRow{Value: controllerLabel(c), Dim: true})
	}

	disks := ws.attachedDisks()
	rows = append(rows, fbui.CardRow{
		Label: "디스크",
		Value: fmt.Sprintf("%d 개", len(disks)),
	})
	for _, d := range disks {
		rows = append(rows, fbui.CardRow{Value: d, Dim: true})
	}

	rows = append(rows, fbui.CardRow{
		Label: "랜카드",
		Value: fmt.Sprintf("%d 개", len(ws.nics)),
	})
	for _, n := range ws.nics {
		rows = append(rows, fbui.CardRow{Value: nicLabel(n), Dim: true})
	}
	return rows
}

// controllerLabel is one controller on one line: PCI address, kind, port count.
// controllerLabel - 컨트롤러 한 대를 한 줄로. PCI 주소와 종류, 포트 수.
func controllerLabel(c hwscan.Controller) string {
	kind := "저장소"
	// The top 16 bits of the PCI class are the kind, the byte below that the
	// specific mode.
	//
	// PCI class 의 위 16 비트가 종류, 그 아래 바이트가 세부 모드다.
	switch c.Class >> 8 {
	case 0x0106:
		kind = "SATA"
		if c.Class&0xFF == 0x01 {
			kind = "AHCI"
		}
	case 0x0107:
		kind = "SAS"
	case 0x0108:
		kind = "NVMe"
	case 0x0100:
		kind = "SCSI"
	case 0x0104:
		// LSI MegaRAID, Adaptec, Areca. Naming the family matters on this
		// screen: a card shown only as "storage" tells the user nothing about
		// which driver has to be present.
		//
		// LSI MegaRAID, Adaptec, Areca. 이 화면에서는 계열 이름이 중요하다.
		// "저장소" 로만 뜨면 어떤 드라이버가 있어야 하는지 알 수 없다.
		kind = "RAID"
	}
	return fmt.Sprintf("%s  %s  %d 포트", c.Address, kind, len(c.Ports))
}

// attachedDisks lists the attached disks as "model capacity (kernel name)".
// attachedDisks - 붙어있는 디스크를 "모델 용량 (커널 이름)" 으로.
func (ws *wizardState) attachedDisks() []string {
	var out []string
	for _, c := range ws.controllers {
		for _, port := range c.Ports {
			if port.Block == "" {
				continue
			}
			out = append(out, fmt.Sprintf("%s  %s  (%s)",
				diskModelText(port.Block), diskSizeText(port.Block), port.Block))
		}
	}
	return out
}

// nicLabel is one network card as "device driver MAC".
// nicLabel - 랜카드 한 개를 "장치 드라이버 MAC" 으로.
func nicLabel(n nicInfo) string {
	parts := n.Name
	if n.Driver != "" {
		parts += "  " + n.Driver
	}
	if n.MAC != "" {
		parts += "  " + n.MAC
	}
	return parts
}

// diskCounts is how many disks can be used for storage, and how many ports
// there are in all.
//
// diskCounts - 저장소로 쓸 수 있는 디스크 수와 전체 포트 수.
func (ws *wizardState) diskCounts() (disks, ports int) {
	for _, c := range ws.controllers {
		for _, port := range c.Ports {
			ports++
			if port.Block != "" {
				disks++
			}
		}
	}
	return disks, ports
}

// buildModel picks the model.
//
// The DSM version is not offered. The only verified combinations are on the
// catalog.TargetProductVersion line, so opening the version up would only add
// combinations nobody has tried. Picking a model attaches that line's newest
// release automatically.
//
// buildModel - 모델을 고른다.
//
// DSM 버전은 고르게 하지 않는다. 검증된 조합은 catalog.TargetProductVersion
// 계열 하나뿐이라, 고를 수 있게 열어두면 시험된 적 없는 조합만 늘어난다.
// 모델을 고르면 그 계열의 최신 릴리스가 자동으로 붙는다.
func (ws *wizardState) buildModel(w *fbui.Wizard, p *fbui.Panel, body fbui.Rect) {
	th := w.Theme()
	f := fbui.NewForm(body, th)
	cfg, cat := ws.st.cfg, ws.st.cat

	models := catalog.CuratedModels
	sel := -1
	for i, m := range models {
		if strings.EqualFold(m, cfg.Model) {
			sel = i
		}
	}

	dd := fbui.NewDropdown(models, sel, func(i int) {
		cfg.Model = models[i]
		// After a model change, a serial made under the old model's rules is
		// no longer usable.
		//
		// 모델이 바뀌면 이전 모델 규칙으로 만든 시리얼은 못 쓴다.
		cfg.Identity.Serial = ""
		cfg.Identity.MACs = nil
		ws.applyTargetRelease()
		// Rebuild the body so the card shows the new model's details.
		// MarkDirty alone leaves the card widget already built holding the
		// old values.
		//
		// 카드가 새 모델 정보를 보여주도록 본문을 다시 짓는다. MarkDirty
		// 만으로는 이미 만들어진 카드 위젯이 옛 값을 그대로 들고 있다.
		w.RebuildBody()
	})
	dd.Placeholder = "모델을 고르세요"
	f.LabelW = 150
	f.RowWidth("모델", dd, 320, 0)

	if cfg.Model != "" {
		ws.applyTargetRelease()
	}
	f.Space(th.Pad)

	if cfg.Model == "" {
		f.Text("모델을 고르면 그 모델의 정보가 여기에 나옵니다.", true)
		p.Add(f.Widgets()...)
		return
	}

	rows := []fbui.CardRow{}
	if plat, ok := cat.PlatformForModel(cfg.Model); ok && plat != nil {
		rows = append(rows, fbui.CardRow{Label: "플랫폼", Value: plat.Name})
		if k := plat.Kernels[catalog.TargetProductVersion]; k != "" {
			rows = append(rows, fbui.CardRow{Label: "커널", Value: k})
		}
	}
	if v := cfg.DSM.Version; v != "" {
		rows = append(rows, fbui.CardRow{Label: "설치할 DSM", Value: v, Accent: true})
	} else {
		rows = append(rows, fbui.CardRow{
			Label: "설치할 DSM",
			Value: "DSM " + catalog.TargetProductVersion + " 릴리스를 찾지 못했습니다",
			Dim:   true,
		})
	}
	if _, ok := cat.SerialRule(cfg.Model); ok {
		rows = append(rows, fbui.CardRow{Label: "시리얼 규칙", Value: "있음"})
	} else {
		rows = append(rows, fbui.CardRow{Label: "시리얼 규칙", Value: "없음", Dim: true})
	}
	f.Full(fbui.NewCard(cfg.Model, rows...), fbui.CardHeight(th, cfg.Model, len(rows)))

	f.Space(th.Pad)
	if ws.embedded[cfg.Model] {
		f.Text("이 모델의 DSM 커널은 이미지 안에 들어있습니다.", true)
	} else {
		f.Text("이 모델의 DSM 커널은 설치 단계에서 시놀로지 서버에서 받습니다.", true)
	}
	p.Add(f.Widgets()...)
}

// applyTargetRelease puts the chosen model's target release into the settings.
//
// Skipping on the version string alone would be wrong. All three supported
// models are on the same DSM version ("7.4.1-90080"), so changing the model
// leaves the version unchanged. Comparing versions only would leave the URL and
// MD5 belonging to the previous model, and installing in that state fetches and
// uses another model's kernel - picking DS3622xs+ and getting SA6400's 5.10
// kernel, say. So the comparison is against the per-model release itself.
//
// applyTargetRelease - 고른 모델의 설치 대상 릴리스를 설정에 넣는다.
//
// 버전 문자열만 보고 건너뛰면 안 된다. 지원하는 세 모델이 전부 같은 DSM
// 버전("7.4.1-90080")이라, 모델을 바꿔도 버전은 그대로다. 버전만 비교하면
// URL·MD5 가 이전 모델 것으로 남고, 그 상태로 설치하면 고른 모델과 다른
// 모델의 커널을 받아서 쓴다 (예: DS3622xs+ 를 골랐는데 SA6400 의 5.10
// 커널이 깔림). 그래서 모델별 릴리스 자체와 비교한다.
func (ws *wizardState) applyTargetRelease() {
	cfg := ws.st.cfg
	v := ws.st.cat.TargetRelease(cfg.Model)
	if v == "" {
		return
	}
	rel, _ := ws.st.cat.ReleaseFor(cfg.Model, v)
	if cfg.DSM.Version == v && cfg.DSM.URL == rel.URL && cfg.DSM.MD5 == rel.MD5 {
		return
	}
	ws.applyVersion(v)
}

// applyVersion puts the URL and MD5 that go with the chosen version into the
// settings.
//
// applyVersion - 고른 버전에 딸린 URL·MD5 를 설정에 넣는다.
func (ws *wizardState) applyVersion(v string) {
	cfg := ws.st.cfg
	cfg.DSM.Version = v
	rel, _ := ws.st.cat.ReleaseFor(cfg.Model, v)
	cfg.DSM.URL = rel.URL
	cfg.DSM.MD5 = rel.MD5
	ws.st.dirty = true
}

func (ws *wizardState) validateModel() string {
	cfg := ws.st.cfg
	if strings.TrimSpace(cfg.Model) == "" {
		return "모델을 고르세요."
	}
	if strings.TrimSpace(cfg.DSM.Version) == "" {
		return cfg.Model + " 의 DSM " + catalog.TargetProductVersion + " 릴리스를 찾지 못했습니다. 다른 모델을 고르세요."
	}
	if strings.TrimSpace(cfg.DSM.URL) == "" {
		return "이 릴리스의 다운로드 주소가 없습니다. 다른 모델을 고르세요."
	}
	return ""
}

// buildBootDisk shows what is known about the disk the loader is on.
// buildBootDisk - 로더가 들어있는 디스크 정보.
func (ws *wizardState) buildBootDisk(w *fbui.Wizard, p *fbui.Panel, body fbui.Rect) {
	th := w.Theme()
	f := fbui.NewForm(body, th)
	f.LabelW = 200

	if ws.st.disk.Kernel == "" {
		f.Text("로더 디스크를 찾지 못했습니다.", false)
		f.Space(6)
		f.Text("설치는 계속할 수 있지만 결과를 쓸 곳이 없어 실패합니다.", true)
		p.Add(f.Widgets()...)
		return
	}

	f.Row("디스크", roText("/dev/"+ws.st.disk.Kernel), 0)
	f.Row("크기", roText(diskSizeText(ws.st.disk.Kernel)), 0)
	f.Row("모델", roText(diskModelText(ws.st.disk.Kernel)), 0)
	f.Space(6)
	f.Separator()
	f.Text("파티션", false)
	f.Space(4)
	f.Row("1", roText("VIBELDR1 - 부팅(GRUB)과 이 설치 프로그램, 설정 파일"), 0)
	p2 := "VIBELDR2 - DSM 원본 커널과 램디스크"
	if n := len(ws.embedded); n > 0 {
		p2 = fmt.Sprintf("VIBELDR2 - DSM 원본 (이미지에 %d 모델 포함)", n)
	}
	f.Row("2", roText(p2), 0)
	f.Row("3", roText("VIBELDR3 - 패치된 DSM 커널과 램디스크 (설치가 여기에 씀)"), 0)
	f.Row("4", roText("드라이버 팩 (파일시스템 없는 영역)"), 0)
	f.Space(8)
	f.Text("이 디스크는 DSM 의 저장소 목록에 나타나지 않습니다.", true)

	// The loader has to be on USB. DSM identifies its boot disk by USB - the vid
	// and pid on the cmdline - and creates /dev/synoboot* from that. On any other
	// bus those never appear and the DSM install fails at its last step, mounting
	// the boot partition. That failure only shows up after the whole install has
	// run, so it is said here in advance.
	//
	// 로더는 USB 에 있어야 한다. DSM 은 부트 디스크를 USB(cmdline 의 vid/pid)
	// 로 식별해 /dev/synoboot* 를 만드는데, 다른 버스에 있으면 그게 안 생기고
	// DSM 설치가 마지막 단계(부트 파티션 마운트)에서 실패한다. 설치를 다
	// 돌린 뒤에야 알게 되는 실패라, 여기서 미리 알려준다.
	if bus := loaderBus(ws.st.disk.Kernel); bus != "usb" {
		f.Space(6)
		where := "USB 가 아닌 곳"
		if bus != "" {
			where = bus
		}
		f.Text("주의: 이 로더가 "+where+" 에 있습니다. USB 에 꽂아야 합니다.", false)
		f.Space(4)
		f.Text("DSM 은 USB 로더만 부트 디스크로 인식합니다. 이대로 두면 로더 설치는 되지만", true)
		f.Text("DSM 본 설치가 마지막에 실패합니다.", true)
	}
	p.Add(f.Widgets()...)
}

// diskSizeText turns sysfs's sector count into a size a person can read.
// diskSizeText - sysfs 의 섹터 수를 사람이 읽는 크기로.
func diskSizeText(kernelName string) string {
	b, err := os.ReadFile("/sys/block/" + kernelName + "/size")
	if err != nil {
		return "알 수 없음"
	}
	var sectors int64
	if _, err := fmt.Sscan(strings.TrimSpace(string(b)), &sectors); err != nil {
		return "알 수 없음"
	}
	return humanBytes(sectors * 512)
}

// diskModelText is the disk model name sysfs knows.
// diskModelText - sysfs 가 아는 디스크 모델명.
func diskModelText(kernelName string) string {
	b, err := os.ReadFile("/sys/block/" + kernelName + "/device/model")
	if err != nil {
		return "알 수 없음"
	}
	s := strings.TrimSpace(string(b))
	if s == "" {
		return "알 수 없음"
	}
	return s
}

// humanBytes turns a byte count into GB or TB.
// humanBytes - 바이트 수를 GB/TB 로.
func humanBytes(n int64) string {
	const unit = 1000
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 4; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "kMGTP"[exp])
}

// buildStorage decides which disk goes in each of the model's bays.
//
// The rows are bays, not ports. The bay count is the model's (4 on DS918+, 12
// on DS3622xs+), so the number of rows is the same however many SATA ports the
// board has. Making ports the axis would show twelve bays that do not exist
// when a 4-bay model runs on a machine with twelve ports.
//
// The result gathers in the settings' BayOrder - the port keys in bay 1, 2, 3
// order - and goes out from there two ways:
//
//   - non-DT platforms (DS918+, DS3622xs+): the SataPortMap and DiskIdxMap
//     kernel parameters
//   - DT platforms (SA6400): /vibeldr-bay-plan, carried in the ramdisk, which
//     vibeldr-init reads at boot to rewrite model.dtb in that order
//
// buildStorage - 모델의 베이마다 어떤 디스크를 넣을지 정한다.
//
// 줄의 축은 포트가 아니라 베이다. 베이 수는 모델이 정하는 값이라
// (DS918+ 4, DS3622xs+ 12) 메인보드에 SATA 포트가 몇 개든 줄 수는 그대로다.
// 포트를 축으로 삼으면 포트 12 개짜리 기계에 4 베이 모델을 올렸을 때
// 있지도 않은 베이 12 개가 보이게 된다.
//
// 고른 결과는 설정의 BayOrder (베이 1, 2, 3 … 순서의 포트 키 목록) 로
// 모이고, 거기서 위 영문의 두 갈래로 나간다.
func (ws *wizardState) buildStorage(w *fbui.Wizard, p *fbui.Panel, body fbui.Rect) {
	th := w.Theme()
	f := fbui.NewForm(body, th)

	if len(ws.controllers) == 0 {
		f.Text("디스크 컨트롤러를 찾지 못했습니다.", false)
		f.Space(6)
		f.Text("드라이버가 아직 안 올라왔거나 이 기계에 SATA 컨트롤러가 없습니다.", true)
		p.Add(f.Widgets()...)
		return
	}

	ports := ws.allPorts()
	if len(ports) == 0 {
		f.Text("쓸 수 있는 포트가 없습니다.", false)
		p.Add(f.Widgets()...)
		return
	}

	// What can go in a bay: a port with a disk actually attached that is not
	// the loader.
	//
	// 베이에 넣을 수 있는 후보: 디스크가 실제로 붙어 있고 로더가 아닌 포트.
	var cand []portInfo
	var loader *portInfo
	for i, pi := range ports {
		switch {
		case pi.loader:
			loader = &ports[i]
		case pi.block != "":
			cand = append(cand, pi)
		}
	}

	// The model decides the bay count. For a model that is not known, only as
	// many bays as there are attached disks - guessing more would invent bays
	// that do not exist.
	//
	// 베이 수는 모델이 정한다. 모르는 모델이면 붙어 있는 디스크 수만큼만
	// 만든다 - 추측해서 늘리면 없는 베이가 생긴다.
	maxBays := ws.st.cfg.MaxBays()
	guessed := false
	if maxBays <= 0 {
		maxBays, guessed = len(cand), true
	}
	if maxBays <= 0 {
		f.Text("배치할 디스크가 없습니다.", false)
		f.Space(6)
		f.Text("로더를 제외하면 디스크가 붙은 포트가 하나도 없습니다.", true)
		p.Add(f.Widgets()...)
		return
	}

	// With the settings empty, fill from the first bay in detection order.
	// 설정이 비어 있으면 감지 순서대로 앞 베이부터 채운다.
	if len(ws.st.cfg.Storage.BayOrder) == 0 && len(cand) > 0 {
		ws.st.cfg.Storage.BayOrder = defaultBayOrder(cand, maxBays)
		ws.st.dirty = true
	}
	order := ws.bayOrderSized(maxBays)

	// A port another bay already took is left out of this bay's list.
	// 이미 다른 베이가 가져간 포트는 이 베이의 목록에서 뺀다.
	taken := map[string]int{}
	for i, k := range order {
		if k != "" {
			taken[k] = i + 1
		}
	}

	weights := []int{2, 7, 3, 5}
	f.Columns(24, weights, []fbui.Widget{
		dimText("베이"), dimText("디스크"), dimText("크기"), dimText("포트"),
	})
	f.Separator()

	// When the bays do not fit on one screen, as on a 12-bay model, they are
	// split into pages.
	//
	// 베이가 한 화면에 안 들어가면 (12 베이 모델) 페이지로 나눈다.
	start, end, pages := ws.storagePageRange(f, th, maxBays)

	for bay := start + 1; bay <= end; bay++ {
		cur := order[bay-1]

		choices := []string{"비움"}
		keys := []string{""}
		sel := 0
		for _, pi := range cand {
			if n, ok := taken[pi.key]; ok && n != bay {
				continue
			}
			choices = append(choices, diskChoiceText(pi))
			keys = append(keys, pi.key)
			if pi.key == cur {
				sel = len(choices) - 1
			}
		}

		size, where := "-", "-"
		if pi, ok := findPort(cand, cur); ok {
			size = diskSizeText(pi.block)
			where = fmt.Sprintf("%s  %s", pi.portName, shortPCI(pi.pcieRoot))
		}

		dd := fbui.NewDropdown(choices, sel, nil)
		kk, b := keys, bay
		dd.OnChange = func(i int) {
			key := ""
			if i >= 0 && i < len(kk) {
				key = kk[i]
			}
			ws.setBayPort(b, key, maxBays)
			// Rebuild the body so the other bays' candidate lists update too.
			// 다른 베이의 후보 목록도 갱신되도록 본문을 다시 짓는다.
			w.RebuildBody()
		}

		f.Columns(th.RowH, weights, []fbui.Widget{
			roText(fmt.Sprintf("%d", bay)), dd, roText(size), roText(where),
		})
	}

	if pages > 1 {
		f.Space(4)
		prev := fbui.NewButton("이전", func() {
			ws.storagePage--
			w.RebuildBody()
		})
		prev.SetDisabled(ws.storagePage == 0)
		next := fbui.NewButton("다음", func() {
			ws.storagePage++
			w.RebuildBody()
		})
		next.SetDisabled(ws.storagePage >= pages-1)
		mid := roText(fmt.Sprintf("베이 %d-%d / %d  (%d/%d 쪽)",
			start+1, end, maxBays, ws.storagePage+1, pages))
		mid.Align = 0 // centred / 가운데
		f.Columns(th.RowH, []int{3, 8, 3}, []fbui.Widget{prev, mid, next})
	}

	f.Space(6)
	if guessed {
		f.Text(fmt.Sprintf("%s 의 베이 수를 몰라서 붙어 있는 디스크 수(%d)만큼 만들었습니다.",
			ws.st.cfg.Model, maxBays), true)
	} else {
		f.Text(fmt.Sprintf("%s 는 베이가 %d 개입니다. 적힌 번호가 DSM 저장소 관리자에 보이는 순서입니다.",
			ws.st.cfg.Model, maxBays), true)
	}
	if loader != nil {
		f.Space(2)
		f.Text(fmt.Sprintf("%s (%s) 는 부트로더 디스크라 베이에 들어가지 않습니다.",
			loader.portName, diskModelText(loader.block)), true)
	}
	p.Add(f.Widgets()...)
}

// diskChoiceText is one disk as it appears in a dropdown.
// diskChoiceText - 드롭다운에 보일 디스크 한 줄.
func diskChoiceText(pi portInfo) string {
	return fmt.Sprintf("%s  %s", pi.portName, diskModelText(pi.block))
}

// findPort finds a candidate by its port key.
// findPort - 포트 키로 후보를 찾는다.
func findPort(cand []portInfo, key string) (portInfo, bool) {
	if key == "" {
		return portInfo{}, false
	}
	for _, pi := range cand {
		if pi.key == key {
			return pi, true
		}
	}
	return portInfo{}, false
}

// bayOrderSized returns a copy of the settings' BayOrder resized to the bay
// count.
//
// A short one is padded with blanks and a long one is cut, as happens when the
// model changes from 12 bays to 4. The settings themselves are untouched - this
// is only for drawing the screen, and the real change happens in setBayPort
// alone.
//
// bayOrderSized - 설정의 BayOrder 를 베이 수에 맞춘 사본으로 돌려준다.
//
// 짧으면 빈 칸으로 늘리고, 길면 자른다 (모델을 12 베이에서 4 베이로 바꾼
// 경우). 설정 자체는 건드리지 않는다 - 화면을 그리는 것뿐이고, 실제 변경은
// setBayPort 에서만 일어난다.
func (ws *wizardState) bayOrderSized(maxBays int) []string {
	out := make([]string, maxBays)
	for i, k := range ws.st.cfg.Storage.BayOrder {
		if i >= maxBays {
			break
		}
		out[i] = strings.TrimSpace(k)
	}
	return out
}

// storagePageRange is the range of ports to draw now, and the total page count.
//
// It first counts how many rows fit in the vertical space left, and if the list
// is longer than that it counts again with room taken out for the page-move row
// and the hint line. If ws.storagePage is out of range, as it is once the ports
// shrink, it is pulled back here.
//
// storagePageRange - 지금 그릴 포트 구간과 전체 쪽 수.
//
// 남은 세로 공간에서 몇 줄이 들어가는지 먼저 세고, 목록이 그보다 길면
// 페이지 이동 줄과 안내 문구가 놓일 자리를 빼고 다시 센다. ws.storagePage
// 가 범위를 벗어나 있으면 (포트가 줄었을 때) 여기서 끌어당긴다.
func (ws *wizardState) storagePageRange(f *fbui.Form, th *fbui.Theme, n int) (start, end, pages int) {
	rowH := th.RowH + f.Gap
	// The one row that must be left at the bottom: the hint line.
	// 바닥에 반드시 남겨야 하는 자리: 안내 문구 한 줄.
	footer := th.FontSmall.Height() + 4 + 6
	perPage := (f.Remaining() - footer) / rowH
	if n > perPage {
		// The page-move row takes space too.
		// 페이지 이동 줄도 자리를 차지한다.
		perPage = (f.Remaining() - footer - rowH - 4) / rowH
	}
	if perPage < 1 {
		perPage = 1
	}
	pages = (n + perPage - 1) / perPage
	if pages < 1 {
		pages = 1
	}
	if ws.storagePage >= pages {
		ws.storagePage = pages - 1
	}
	if ws.storagePage < 0 {
		ws.storagePage = 0
	}
	start = ws.storagePage * perPage
	end = start + perPage
	if end > n {
		end = n
	}
	return start, end, pages
}

// portInfo is one port, as one row on the screen.
// portInfo - 화면에 한 줄로 그릴 포트 하나.
type portInfo struct {
	pcieRoot string
	portName string
	// index is the port number within the controller, the value written into
	// the settings.
	//
	// index - 컨트롤러 안에서의 포트 번호. 설정에 적히는 값.
	index uint32
	// block is the kernel name of the attached disk; empty means an empty port.
	// block - 붙어있는 디스크의 커널 이름. 비었으면 빈 포트.
	block string
	// key is "<PCIe path> <port number>", the same shape as a settings line.
	// key - "<PCIe 경로> <포트 번호>". 설정 줄과 같은 형태.
	key string
	// loader says whether the loader disk is on this port. A loader plugged
	// into SATA shows up in the controller list too, but it is for booting
	// rather than a storage bay and must not be given a bay number - DSM also
	// sees this disk as a DOM and leaves it out of the bays.
	//
	// loader - 이 포트에 로더 디스크가 붙어 있는지. 로더가 SATA 에 꽂혀
	// 있으면 이 포트도 컨트롤러 목록에 나오는데, 그건 저장소 베이가 아니라
	// 부팅용이라 베이 번호를 주면 안 된다 (DSM 도 이 디스크를 DOM 으로 보고
	// 베이에서 뺀다).
	loader bool
}

// allPorts lays every port of every detected controller out as a candidate for
// the bay order.
//
// allPorts - 감지된 컨트롤러의 모든 포트를 베이 순서 후보로 늘어놓는다.
func (ws *wizardState) allPorts() []portInfo {
	var out []portInfo
	for _, c := range ws.controllers {
		for _, port := range c.Ports {
			out = append(out, portInfo{
				pcieRoot: c.PCIeRoot,
				portName: port.Name,
				index:    port.Index,
				block:    port.Block,
				key:      fmt.Sprintf("%s %d", c.PCIeRoot, port.Index),
				loader:   port.Block != "" && port.Block == ws.st.disk.Kernel,
			})
		}
	}
	return out
}

// defaultBayOrder fills from the first bay with the candidate disks in
// detection order. Disks beyond the model's bay count are left unplaced.
//
// defaultBayOrder - 후보 디스크를 감지 순서대로 앞 베이부터 채운다.
// 모델의 베이 수보다 디스크가 많으면 넘치는 만큼은 배치하지 않는다.
func defaultBayOrder(cand []portInfo, maxBays int) []string {
	var out []string
	for _, p := range cand {
		if len(out) >= maxBays {
			break
		}
		out = append(out, p.key)
	}
	return out
}

// setBayPort puts a port in one bay; an empty key empties that bay.
//
// BayOrder is positional: entry i is bay i+1, and an empty string means an
// empty bay. So emptying a bay in the middle does not pull the later bay
// numbers forward - portMapFor on the cmdline side and applyBayPlan on the dtb
// side both skip empty entries. The port is cleared from anywhere else so the
// same one never lands in two bays.
//
// Trailing empty bays are cut when it is saved; leaving them piles meaningless
// blank lines into the settings file.
//
// setBayPort - 베이 하나에 포트를 놓는다. key 가 빈 문자열이면 그 베이를
// 비운다.
//
// BayOrder 는 자리 기반이다: i 번째 항목이 곧 베이 i+1 이고, 빈 문자열은
// 빈 베이를 뜻한다. 그래서 가운데 베이를 비워도 뒤 베이 번호가 당겨지지
// 않는다 (cmdline 쪽 portMapFor 와 dtb 쪽 applyBayPlan 둘 다 빈 항목을
// 건너뛴다). 같은 포트가 두 베이에 들어가지 않도록 다른 자리에서는 지운다.
//
// 뒤쪽 빈 베이는 저장할 때 잘라낸다. 남겨두면 설정 파일에 의미 없는 빈
// 줄이 쌓인다.
func (ws *wizardState) setBayPort(bay int, key string, maxBays int) {
	order := ws.bayOrderSized(maxBays)
	if bay < 1 || bay > len(order) {
		return
	}
	if key != "" {
		for i := range order {
			if order[i] == key {
				order[i] = ""
			}
		}
	}
	order[bay-1] = key
	for len(order) > 0 && order[len(order)-1] == "" {
		order = order[:len(order)-1]
	}
	ws.st.cfg.Storage.BayOrder = order
	ws.st.dirty = true
}

// validateStorage checks that at least one bay was filled. Duplicates are
// prevented by the UI in the first place - each port only offers the numbers
// still free - so only the count is checked here.
//
// validateStorage - 최소 한 개는 배치했는지 본다. 중복은 UI 가 애초에
// 막으므로(포트마다 남은 번호만 노출) 여기서는 개수만 확인한다.
func (ws *wizardState) validateStorage() string {
	n := 0
	for _, k := range ws.st.cfg.Storage.BayOrder {
		if strings.TrimSpace(k) != "" {
			n++
		}
	}
	if n == 0 {
		return "디스크를 하나도 배치하지 않았습니다. 최소 한 개는 베이에 놓으세요."
	}
	return ""
}

// shortPCI shows only the tail of a long PCIe path, to keep the table from
// being pushed out of shape.
//
// shortPCI - PCIe 경로가 길면 뒤쪽만 보여준다. 표가 밀리지 않게.
func shortPCI(s string) string {
	if i := strings.LastIndex(s, ","); i >= 0 && i+1 < len(s) {
		return "…" + s[i:]
	}
	return s
}

// buildNetwork lists the network cards and sets the MAC addresses.
//
// A MAC address is not derived from the serial number; the two have nothing to
// do with one another. The default is to use the address each card already has,
// and choosing to set them by hand fills each field by typing or from the
// randomise button. Random means the chosen model's Synology OUI prefix with
// random digits after it.
//
// An address set by hand is told to DSM through the mac1..N kernel parameters
// and, at boot, written into the card's own hardware address by vibeldr-init.
// So the address entered here is also what shows up in the router's DHCP list.
//
// buildNetwork - 랜카드 목록과 MAC 주소 지정.
//
// MAC 주소는 시리얼에서 계산되지 않는다. 둘은 서로 관계가 없다. 기본은
// 랜카드가 원래 가진 주소를 그대로 쓰는 것이고, 직접 지정을 고르면 칸마다
// 손으로 넣거나 무작위 생성 단추로 채운다. 무작위는 고른 모델의 시놀로지
// OUI 접두사에 임의의 뒷자리를 붙인다.
//
// 직접 지정한 주소는 커널 파라미터 mac1..N 으로 DSM 에 알리는 동시에,
// 부팅 때 vibeldr-init 이 랜카드 하드웨어 주소 자체에 박는다. 그래서 공유기
// DHCP 목록에도 여기 적은 주소가 뜬다.
func (ws *wizardState) buildNetwork(w *fbui.Wizard, p *fbui.Panel, body fbui.Rect) {
	th := w.Theme()
	f := fbui.NewForm(body, th)
	cfg := ws.st.cfg

	if len(ws.nics) == 0 {
		f.Text("랜카드를 찾지 못했습니다.", false)
		f.Space(6)
		f.Text("네트워크가 없으면 DSM 을 활성화할 수 없습니다.", true)
		p.Add(f.Widgets()...)
		return
	}

	// The NIC count is what decides how many are announced to DSM.
	// NIC 개수는 DSM 에 몇 개를 알릴지에 쓰인다.
	if cfg.Identity.NICCount != len(ws.nics) {
		cfg.Identity.NICCount = len(ws.nics)
		ws.st.dirty = true
	}

	manual := cfg.Identity.ForceMAC
	if !manual {
		// Even where nothing is forced, the address announced to DSM has to
		// be the card's real one. Leaving it empty makes the build invent a
		// random address for the cmdline, which then disagrees with what the
		// card actually has.
		//
		// 강제하지 않더라도 DSM 에 알리는 주소는 랜카드의 실제 주소여야
		// 한다. 비워두면 빌드가 무작위 주소를 만들어 cmdline 에 넣는 바람에
		// 카드가 실제로 가진 주소와 어긋난다.
		ws.useRealMACs()
	}

	f.Full(fbui.NewRadioGroup([]string{
		"감지된 실제 MAC 주소를 그대로 사용",
		"MAC 주소를 직접 지정",
	}, boolIdx(manual), func(i int) {
		cfg.Identity.ForceMAC = i == 1
		if i == 0 {
			ws.useRealMACs()
		} else {
			ws.ensureMACs()
		}
		ws.st.dirty = true
		w.RebuildBody()
	}), th.RowH*2)

	f.Space(10)
	f.Separator()

	weights := []int{4, 4, 6, 3}
	f.Columns(24, weights, []fbui.Widget{
		dimText("장치"), dimText("드라이버"), dimText("MAC 주소"), dimText("DSM"),
	})
	f.Separator()

	ws.macFields = ws.macFields[:0]
	for i, n := range ws.nics {
		var macCell fbui.Widget
		if manual {
			idx := i
			tf := fbui.NewTextField(ws.macAt(idx), func(s string) {
				ws.setMAC(idx, s)
			})
			tf.Upper = false
			tf.MaxLen = 17
			tf.Placeholder = n.MAC
			tf.Filter = func(r rune) bool {
				return r >= '0' && r <= '9' || r >= 'a' && r <= 'f' ||
					r >= 'A' && r <= 'F' || r == ':' || r == '-'
			}
			tf.Validate = func(s string) string {
				if strings.TrimSpace(s) == "" {
					return "비워둘 수 없습니다"
				}
				if _, err := catalog.NormalizeMAC(s); err != nil {
					return "16진수 12자리여야 합니다"
				}
				return ""
			}
			ws.macFields = append(ws.macFields, tf)
			macCell = tf
		} else {
			macCell = roText(orDash(n.MAC))
		}
		f.Columns(th.RowH, weights, []fbui.Widget{
			roText(n.Name), roText(orDash(n.Driver)), macCell,
			roText(fmt.Sprintf("eth%d", i)),
		})
	}

	f.Space(6)
	if manual {
		f.Full(fbui.NewButton("무작위 생성", func() {
			macs, err := ws.st.cat.GenerateMACs(cfg.Model, len(ws.nics))
			if err != nil {
				return
			}
			cfg.Identity.MACs = macs
			ws.st.dirty = true
			w.RebuildBody()
		}), th.RowH)
		f.Space(4)
		f.Text("무작위 생성은 고른 모델의 시놀로지 OUI 접두사에 임의의 뒷자리를 붙입니다. 시리얼과는 관계가 없습니다.", true)
		f.Space(2)
		f.Text("여기 적은 주소는 랜카드 자체에 박힙니다. 공유기 DHCP 목록과 find.synology 에도 이 주소로 보입니다.", true)
		f.Space(2)
		f.Text("주소를 바꾸면 공유기의 DHCP 예약도 새 주소로 다시 잡아야 합니다.", true)
	} else {
		f.Text("랜카드가 원래 가진 주소를 그대로 씁니다. 공유기의 DHCP 예약이 그대로 유지됩니다.", true)
	}
	p.Add(f.Widgets()...)
}

// boolIdx is the 0 or 1 used as a radio option's index.
// boolIdx - 라디오 선택지 번호로 쓰는 0/1.
func boolIdx(b bool) int {
	if b {
		return 1
	}
	return 0
}

// useRealMACs puts the detected cards' real addresses into the settings as they
// are.
//
// useRealMACs - 감지된 랜카드의 실제 주소를 설정에 그대로 넣는다.
func (ws *wizardState) useRealMACs() {
	cfg := ws.st.cfg
	macs := make([]string, 0, len(ws.nics))
	for _, n := range ws.nics {
		m, err := catalog.NormalizeMAC(n.MAC)
		if err != nil {
			// If any card's address could not be read, empty the list and let
			// the build make them.
			//
			// 주소를 못 읽은 카드가 있으면 목록을 비워 빌드가 만들게 둔다.
			return
		}
		macs = append(macs, m)
	}
	if equalStrings(cfg.Identity.MACs, macs) {
		return
	}
	cfg.Identity.MACs = macs
	ws.st.dirty = true
}

// equalStrings reports whether two string lists are the same.
// equalStrings - 두 문자열 목록이 같은지.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ensureMACs builds the starting values for the fields when switching to
// setting them by hand. Where a real detected address exists, that is the
// starting point.
//
// ensureMACs - 직접 지정으로 넘어갈 때 칸을 채울 초기값을 만든다.
// 감지된 실제 주소가 있으면 그걸 출발점으로 삼는다.
func (ws *wizardState) ensureMACs() {
	cfg := ws.st.cfg
	if len(cfg.Identity.MACs) >= len(ws.nics) {
		return
	}
	macs := make([]string, len(ws.nics))
	copy(macs, cfg.Identity.MACs)
	for i := range macs {
		if macs[i] != "" {
			continue
		}
		if m, err := catalog.NormalizeMAC(ws.nics[i].MAC); err == nil {
			macs[i] = m
		}
	}
	cfg.Identity.MACs = macs
	ws.st.dirty = true
}

// macAt is the address shown in card i's field.
// macAt - i 번 랜카드 칸에 보일 주소.
func (ws *wizardState) macAt(i int) string {
	cfg := ws.st.cfg
	if i < len(cfg.Identity.MACs) && cfg.Identity.MACs[i] != "" {
		return cfg.Identity.MACs[i]
	}
	return ws.nics[i].MAC
}

// setMAC puts what was typed into a field into the settings. A badly formed
// value is left to the screen's error display and is not saved.
//
// setMAC - 칸에 넣은 값을 설정에 반영한다. 형식이 틀리면 화면의 오류
// 표시에 맡기고 저장은 하지 않는다.
func (ws *wizardState) setMAC(i int, s string) {
	cfg := ws.st.cfg
	for len(cfg.Identity.MACs) < len(ws.nics) {
		cfg.Identity.MACs = append(cfg.Identity.MACs, "")
	}
	if m, err := catalog.NormalizeMAC(s); err == nil {
		cfg.Identity.MACs[i] = m
	} else {
		cfg.Identity.MACs[i] = strings.TrimSpace(s)
	}
	ws.st.dirty = true
}

// validateNetwork requires every field to hold a valid address once setting
// them by hand was chosen.
//
// validateNetwork - 직접 지정을 골랐으면 모든 칸이 올바른 주소여야 한다.
func (ws *wizardState) validateNetwork() string {
	if !ws.st.cfg.Identity.ForceMAC || len(ws.nics) == 0 {
		return ""
	}
	seen := map[string]bool{}
	for i := range ws.nics {
		m, err := catalog.NormalizeMAC(ws.macAt(i))
		if err != nil {
			return fmt.Sprintf("%s 의 MAC 주소가 올바르지 않습니다", ws.nics[i].Name)
		}
		if seen[m] {
			return fmt.Sprintf("MAC 주소 %s 가 두 번 쓰였습니다", m)
		}
		seen[m] = true
	}
	return ""
}

// prettyMAC breaks 12 hex digits up with colons so they read better.
// prettyMAC - 12 자리 16진수를 콜론으로 끊어 읽기 좋게.
func prettyMAC(s string) string {
	m, err := catalog.NormalizeMAC(s)
	if err != nil {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(m); i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(m[i : i+2])
	}
	return b.String()
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

// buildIdentity is the serial number step.
// buildIdentity - 시리얼 번호.
func (ws *wizardState) buildIdentity(w *fbui.Wizard, p *fbui.Panel, body fbui.Rect) {
	th := w.Theme()
	f := fbui.NewForm(body, th)
	cfg, cat := ws.st.cfg, ws.st.cat

	_, hasRule := cat.SerialRule(cfg.Model)
	if !hasRule {
		f.Text(cfg.Model+" 의 시리얼 규칙이 카탈로그에 없습니다.", false)
		f.Space(6)
		f.Text("직접 넣은 값을 그대로 씁니다.", true)
		f.Space(10)
		ws.serialManual = true
	}

	// On automatic, one is generated and filled in right here. Hiding the
	// value until the install starts would have the user move on without
	// knowing what is going in.
	//
	// 자동이면 여기서 바로 만들어 채운다. 설치가 시작될 때까지 값을
	// 숨겨두면 사용자가 무엇이 들어갈지 모른 채 넘어가게 된다.
	if hasRule && !ws.serialManual && cfg.Identity.Serial == "" {
		ws.newSerial()
	}

	mode := 0
	if ws.serialManual {
		mode = 1
	}

	snField := fbui.NewTextField(cfg.Identity.Serial, func(s string) {
		ws.serialText = s
		cfg.Identity.Serial = s
		ws.st.dirty = true
	})
	snField.Upper = true
	snField.MaxLen = 16
	snField.Placeholder = "예: 2270SQRLR3H9M"
	snField.Filter = func(r rune) bool {
		return r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
	}
	snField.Validate = func(s string) string {
		if s == "" {
			return ""
		}
		if err := cat.ValidateSerial(cfg.Model, s); err != nil {
			return err.Error()
		}
		return ""
	}
	snField.SetDisabled(!ws.serialManual)

	if hasRule {
		f.Full(fbui.NewRadioGroup([]string{
			"자동 생성 (모델 규칙에 맞춰 만듭니다)",
			"직접 입력",
		}, mode, func(i int) {
			ws.serialManual = i == 1
			snField.SetDisabled(!ws.serialManual)
			if !ws.serialManual {
				// Switching back to automatic draws a fresh value that fits
				// the rules and shows it.
				//
				// 자동으로 되돌리면 규칙에 맞는 값을 새로 뽑아 보여준다.
				ws.newSerial()
				snField.SetText(cfg.Identity.Serial)
			}
			w.RebuildBody()
		}), th.RowH*2)
		f.Space(10)
	}

	f.LabelW = 180
	f.RowWidth("시리얼 번호", snField, 320, 0)

	f.Space(8)
	if hasRule && !ws.serialManual {
		f.RowWidth("", fbui.NewButton("다시 생성", func() {
			ws.newSerial()
			snField.SetText(cfg.Identity.Serial)
			w.RebuildBody()
		}), 160, 0)
		f.Space(4)
		f.Text("위 시리얼로 설치합니다. 모델 규칙에 맞춰 만든 값이고, MAC 주소와는 관계가 없습니다.", true)
	} else {
		f.Text("영문과 숫자만 넣을 수 있습니다.", true)
	}
	p.Add(f.Widgets()...)
}

// newSerial draws a fresh serial number fitting the chosen model's rules and
// puts it into the settings. For a model with no rules it does nothing.
//
// newSerial - 고른 모델의 규칙에 맞는 시리얼을 새로 뽑아 설정에 넣는다.
// 규칙이 없는 모델이면 아무것도 하지 않는다.
func (ws *wizardState) newSerial() {
	cfg := ws.st.cfg
	sn, err := ws.st.cat.GenerateSerial(cfg.Model)
	if err != nil {
		return
	}
	cfg.Identity.Serial = sn
	ws.serialText = sn
	ws.st.dirty = true
}

func (ws *wizardState) validateIdentity() string {
	cfg := ws.st.cfg
	if !ws.serialManual {
		return ""
	}
	s := strings.TrimSpace(cfg.Identity.Serial)
	if s == "" {
		return "시리얼 번호를 넣으세요."
	}
	if _, ok := ws.st.cat.SerialRule(cfg.Model); ok {
		if err := ws.st.cat.ValidateSerial(cfg.Model, s); err != nil {
			return "시리얼이 " + cfg.Model + " 규칙에 안 맞습니다: " + err.Error()
		}
	}
	return ""
}

// buildConfirm is the summary shown right before the install.
// buildConfirm - 설치 직전 요약.
func (ws *wizardState) buildConfirm(w *fbui.Wizard, p *fbui.Panel, body fbui.Rect) {
	th := w.Theme()
	f := fbui.NewForm(body, th)
	f.LabelW = 200
	cfg := ws.st.cfg

	plat := ""
	if pl, ok := ws.st.cat.PlatformForModel(cfg.Model); ok && pl != nil {
		plat = "  (" + pl.Name + ")"
	}
	f.Row("모델", roText(cfg.Model+plat), 0)
	f.Row("DSM 버전", roText(cfg.DSM.Version), 0)

	serial := cfg.Identity.Serial
	if serial == "" {
		serial = "설치할 때 자동 생성"
	} else if !ws.serialManual {
		serial += "  (자동 생성)"
	}
	f.Row("시리얼", roText(serial), 0)

	boot := "찾지 못함"
	if ws.st.disk.Kernel != "" {
		boot = "/dev/" + ws.st.disk.Kernel + "  " + diskSizeText(ws.st.disk.Kernel)
	}
	f.Row("부트 디스크", roText(boot), 0)

	disks := 0
	for _, c := range ws.controllers {
		for _, port := range c.Ports {
			if port.Block != "" {
				disks++
			}
		}
	}
	f.Row("저장소 디스크", roText(fmt.Sprintf("%d 대", disks)), 0)

	nics := make([]string, 0, len(ws.nics))
	for i, n := range ws.nics {
		nics = append(nics, fmt.Sprintf("%s→eth%d", n.Name, i))
	}
	f.Row("네트워크", roText(orDash(strings.Join(nics, ", "))), 0)

	macs := make([]string, 0, len(ws.nics))
	for i := range ws.nics {
		macs = append(macs, prettyMAC(ws.macAt(i)))
	}
	how := "실제 주소 그대로"
	if cfg.Identity.ForceMAC {
		how = "직접 지정"
	}
	f.Row("MAC 주소", roText(orDash(strings.Join(macs, ", "))+"  ("+how+")"), 0)

	f.Space(10)
	f.Separator()
	if ws.embedded[cfg.Model] {
		f.Text("다음을 누르면 이미지에 들어있는 DSM 커널을 패치해 부트 디스크에 씁니다.", false)
		f.Space(4)
		f.Text("중간에 전원을 끄지 마세요.", true)
	} else {
		f.Text("다음을 누르면 DSM 이미지를 내려받아 패치하고 부트 디스크에 씁니다.", false)
		f.Space(4)
		f.Text("내려받을 용량이 크니 시간이 걸립니다. 중간에 전원을 끄지 마세요.", true)
	}
	p.Add(f.Widgets()...)
}

// buildInstall is the progress screen; it starts the pipeline the moment it is
// entered.
//
// buildInstall - 진행 화면. 들어오는 즉시 파이프라인을 시작한다.
func (ws *wizardState) buildInstall(w *fbui.Wizard, p *fbui.Panel, body fbui.Rect) {
	th := w.Theme()
	f := fbui.NewForm(body, th)

	// The bar follows the pipeline's stages; startInstall hooks it up.
	// 막대는 파이프라인의 단계를 따라간다. startInstall 이 연결한다.
	bar := &fbui.ProgressBar{Text: "준비 중"}
	ws.bar = bar
	f.Full(bar, 26)
	f.Space(8)

	// All the space left goes to the log window.
	// 남은 공간을 전부 로그 창에 준다.
	h := f.Remaining() - th.Pad
	if h < th.RowH*3 {
		h = th.RowH * 3
	}
	f.Full(ws.log, h)
	p.Add(f.Widgets()...)

	w.SetNextEnabled(false)
	ws.installOnce.Do(ws.startInstall)
}

// validateInstall blocks moving on until the install has finished, and reboots
// once it has.
//
// validateInstall - 설치가 끝나기 전에는 넘어가지 못하게 하고, 끝났으면
// 재부팅한다.
func (ws *wizardState) validateInstall() string {
	ws.mu.Lock()
	done := ws.installDone
	ws.mu.Unlock()
	if !done {
		return "설치가 끝날 때까지 기다리세요."
	}
	syncAll()
	bootIntoDSM(ws.st)
	return ""
}

// startInstall runs the build pipeline on another goroutine and intercepts
// standard output into the log window.
//
// The pipeline was written for the text screen and reports its progress on
// standard output alone. Taking that output through a pipe and moving it onto
// the screen means the same pipeline can be used without splitting the code in
// two.
//
// startInstall - 빌드 파이프라인을 딴 고루틴에서 돌리고, 표준 출력을
// 가로채 로그 창에 흘린다.
//
// 파이프라인은 글자 화면용으로 쓰여 있어서 진행 상황을 표준 출력으로만
// 알린다. 그 출력을 파이프로 받아 화면에 옮기면 코드를 둘로 나누지 않고도
// 같은 파이프라인을 쓸 수 있다.
func (ws *wizardState) startInstall() {
	r, wpipe, err := os.Pipe()
	if err != nil {
		ws.log.Append("설치를 시작할 수 없습니다: " + err.Error())
		ws.finishInstall(true)
		return
	}
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = wpipe, wpipe
	// The overall bar hears of each stage and progress display directly, not
	// by reading the log back.
	// 전체 막대는 단계와 진행률을 로그를 다시 읽지 않고 직접 듣는다.
	ws.progress = newInstallProgress()
	ui.OnSection, ui.OnProgress = ws.progress.section, ws.progress.progress

	go func() {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		// Progress updates the same line with a carriage return and no
		// newline. The default scanner splits on newlines only, so it holds
		// that whole line until the download finishes and the screen looks
		// frozen. Splitting on the carriage return too makes the progress
		// flow into the log window as it happens.
		//
		// 진행률은 개행 없이 캐리지 리턴으로만 같은 줄을 갱신한다. 기본
		// 스캐너는 개행만 끊어서, 다운로드가 끝날 때까지 그 줄을 통째로 물고
		// 있느라 화면이 멈춘 것처럼 보인다. 캐리지 리턴에서도 끊어 진행률이
		// 실시간으로 로그 창에 흐르게 한다.
		sc.Split(fbui.ScanLogLines)
		for sc.Scan() {
			ws.log.Append(stripANSI(sc.Text()))
		}
	}()

	go func() {
		defer func() {
			// Standard output is put back even if the pipeline ends in a
			// panic. Otherwise every later log line goes out through a pipe
			// nobody is reading.
			//
			// 파이프라인이 패닉으로 끝나도 표준 출력을 반드시 되돌린다.
			// 안 그러면 이후 로그가 전부 사라진 파이프로 나간다.
			if rec := recover(); rec != nil {
				ws.log.Append(fmt.Sprintf("설치 중 예외: %v", rec))
				ws.finishInstall(true)
			}
			os.Stdout, os.Stderr = oldOut, oldErr
			ui.OnSection, ui.OnProgress = nil, nil
			_ = wpipe.Close()
		}()
		buildFlow(ws.st, guiBuildIO{ws: ws})
		failed := ws.installFailedFlag()
		if !failed {
			ws.progress.finish()
		}
		ws.finishInstall(failed)
	}()
}

// installFailedFlag - failure is decided by the pipeline's own state flag,
// never by matching text in the log. buildFlow sets st.built only after the
// payload is written and grub has been updated, so the flag cannot be fooled
// by a wording change the way a log-scraping check would be.
//
// installFailedFlag - 실패 여부는 로그 문자열이 아니라 파이프라인의 상태
// 플래그로 판단한다. buildFlow 는 페이로드를 다 쓰고 grub 을 갱신한 뒤에만
// st.built 를 세우므로, 로그 문구를 바꿔도 판정이 흔들리지 않는다.
func (ws *wizardState) installFailedFlag() bool {
	return !ws.st.built
}

func (ws *wizardState) finishInstall(failed bool) {
	ws.mu.Lock()
	ws.installDone = true
	ws.installFailed = failed
	ws.mu.Unlock()
}

// stripANSI takes the escapes out, since the pipeline sends its coloured output
// as it is. The log window has colour rules of its own.
//
// stripANSI - 파이프라인이 색을 입힌 출력을 그대로 보내므로 escape 를
// 걷어낸다. 로그 창은 자기 색 규칙을 따로 쓴다.
func stripANSI(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			// An escape runs until a letter turns up.
			// escape 는 알파벳이 나올 때까지 이어진다.
			j := i + 1
			for j < len(s) && !(s[j] >= 'A' && s[j] <= 'Z' || s[j] >= 'a' && s[j] <= 'z') {
				j++
			}
			i = j + 1
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// guiBuildIO is the pipeline's input and output on the graphical screen.
//
// Unlike the text screen, nothing here waits for the user. The progress screen
// has nowhere to take input, and waiting would make it look frozen. What the
// pipeline meant to say goes into the log instead.
//
// guiBuildIO - 그래픽 화면에서 쓰는 파이프라인 입출력.
//
// 글자 화면과 달리 여기서는 사용자를 기다리지 않는다. 진행 화면에는
// 입력을 받을 자리가 없고, 기다리면 화면이 멈춘 것처럼 보인다. 대신
// 파이프라인이 알리려던 말을 로그에 남긴다.
type guiBuildIO struct{ ws *wizardState }

func (g guiBuildIO) Pause(msg string) {
	if strings.TrimSpace(msg) != "" {
		g.ws.log.Append(msg)
	}
}

// Prompt is never really needed, because the wizard fills every value in
// beforehand. Called anyway, it quietly answers empty. The pipeline takes an
// empty answer as the default - yes, change the boot default - or gives up by
// itself and leaves the reason through Pause. Putting the question in the log
// here would make it look as though the user has something to do.
//
// Prompt - 마법사가 필요한 값을 미리 다 채우므로 물어볼 일이 없다.
// 그래도 불리면 조용히 빈 답을 준다. 빈 답은 파이프라인이 기본값으로
// 받거나(부팅 기본값 변경 = 예) 스스로 접고 Pause 로 이유를 남긴다.
// 여기서 질문을 로그에 남기면 사용자가 할 일이 있는 것처럼 보인다.
func (g guiBuildIO) Prompt(string) string { return "" }

// roText is a label that only shows a value.
// roText - 값을 보여주기만 하는 라벨.
func roText(s string) *fbui.Label { return fbui.NewLabel(s) }

// dimText is a faded label, as used for a list heading.
// dimText - 목록 머리글처럼 흐리게 쓰는 라벨.
func dimText(s string) *fbui.Label {
	l := fbui.NewLabel(s)
	l.Dim = true
	return l
}

// renderBayPlan turns the settings' bay order into the file that goes into the
// ramdisk.
//
// One "<PCIe path> <port number>" per line, and the order they are written in
// is bay 1, 2, 3 and so on. An empty list comes back empty, and then the order
// detected at boot is used as it is.
//
// renderBayPlan - 설정의 베이 순서를 램디스크에 심을 파일 내용으로 만든다.
//
// 한 줄에 "<PCIe 경로> <포트 번호>" 하나씩이고, 적힌 순서가 곧 베이
// 1, 2, 3 … 이다. 목록이 비면 빈 결과를 돌려주고, 그러면 부팅 때 감지한
// 순서가 그대로 쓰인다.
func renderBayPlan(order []string) []byte {
	var b strings.Builder
	for _, line := range order {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if b.Len() == 0 {
		return nil
	}
	return []byte(b.String())
}
