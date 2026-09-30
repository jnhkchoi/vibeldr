// vibeldr is the CLI that builds a Synology DSM loader image from a single
// loader.yaml.
//
// Usage:
//
//	vibeldr <command> [flags]
//
// `vibeldr help` lists the commands.
//
// vibeldr - loader.yaml 하나로 시놀로지 DSM 로더 이미지를 만드는 CLI.
// 사용법은 위와 같고, `vibeldr help` 로 커맨드 목록을 본다.
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"vibeldr/internal/catalog"
	"vibeldr/internal/cmdline"
	"vibeldr/internal/config"
	"vibeldr/internal/dsmconf"
	"vibeldr/internal/hwscan"
	"vibeldr/internal/image"
	"vibeldr/internal/initbin"
	"vibeldr/internal/kpatch"
	"vibeldr/internal/lzma"
	"vibeldr/internal/pat"
	"vibeldr/internal/ramdisk"
	"vibeldr/internal/state"
	"vibeldr/internal/ui"
)

const defaultConfigPath = "loader.yaml"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "validate":
		err = cmdValidate(args)
	case "platforms":
		err = cmdPlatforms(args)
	case "models":
		err = cmdModels(args)
	case "versions":
		err = cmdVersions(args)
	case "identity":
		err = cmdIdentity(args)
	case "cmdline":
		err = cmdCmdline(args)
	case "plan":
		err = cmdPlan(args)
	case "fetch":
		err = cmdFetch(args)
	case "extract":
		err = cmdExtract(args)
	case "patch":
		err = cmdPatch(args)
	case "image":
		err = cmdImage(args)
	case "bootstrap":
		err = cmdBootstrap(args)
	case "status":
		err = cmdStatus(args)
	case "help", "--help", "-h":
		usage()
		return
	default:
		ui.Fail("unknown command %q", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr)
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			ui.Fail("%s", ve.Error())
		} else {
			ui.Fail("%v", err)
		}
		os.Exit(1)
	}
}

func usage() {
	fmt.Printf(`vibeldr - declarative Synology DSM loader builder

USAGE
  vibeldr <command> [flags]

SETUP
  init                 write a starter loader.yaml
  validate             check loader.yaml against the catalog

CATALOG
  platforms            list known platforms
  models [-p PLAT]     list known models
  versions <MODEL>     list DSM versions a model supports

BUILD
  plan                 show exactly what a build would do (changes nothing)
  identity             resolve and pin the serial / MAC addresses
  cmdline              render and validate the DSM kernel command line
  fetch                download the DSM .pat into the cache
  extract              unpack zImage / rd.gz from the cached .pat
  patch                put the disk-mapping helper into the DSM ramdisk
  image                write the 3 partition loader .img (no root needed)
  bootstrap            build a model/DSM-agnostic bootstrap image
                       (generic Alpine kernel + vibeldr-boot; the TUI on
                       first boot picks model/DSM and fills the rest)
  status               show how far the current build has progressed

COMMON FLAGS
  -c PATH              config file (default %s)

`, defaultConfigPath)
}

// ---------------------------------------------------------------------------
// shared helpers
// 공용 유틸
// ---------------------------------------------------------------------------

type env struct {
	cfg        *config.Config
	cat        *catalog.Catalog
	plat       *catalog.Platform
	st         *state.State
	configPath string
	configHash string
}

// load prepares everything a build command needs and fails early with one
// clear message rather than a nil dereference three calls deeper.
//
// load - 빌드 커맨드에 필요한 것을 모두 준비한다. 문제가 있으면 세 호출
// 아래에서 nil 역참조로 죽지 않고, 여기서 분명한 메시지 하나로 일찍 끝낸다.
func load(configPath string) (*env, error) {
	cat, err := catalog.Load()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(configPath, cat)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s not found - run `vibeldr init` first", configPath)
		}
		return nil, err
	}
	plat, ok := cat.PlatformForModel(cfg.Model)
	if !ok {
		return nil, fmt.Errorf("model %q has no platform in the catalog", cfg.Model)
	}
	hash, err := state.HashConfig(configPath)
	if err != nil {
		return nil, err
	}
	st, err := state.Load(cfg.Paths.Work)
	if err != nil {
		return nil, err
	}
	return &env{cfg: cfg, cat: cat, plat: plat, st: st, configPath: configPath, configHash: hash}, nil
}

func configFlag(fs *flag.FlagSet) *string {
	return fs.String("c", defaultConfigPath, "path to loader.yaml")
}

// resolveIdentity pins this configuration's serial number and MAC addresses.
//
// An identity is generated once and kept in the state file. DSM treats a
// changed serial number or MAC as a different machine, so regenerating them on
// every build would reset the QuickConnect registration and the licence
// activations.
//
// resolveIdentity - 이 설정의 시리얼과 MAC 주소를 고정한다.
//
// identity 는 한 번만 생성해 state 에 저장한다. DSM 은 시리얼이나 MAC 이
// 바뀌면 다른 머신으로 취급하므로, 매 빌드마다 재생성하면 QuickConnect
// 등록과 라이선스 활성화가 초기화된다.
func (e *env) resolveIdentity() (cmdline.Identity, bool, error) {
	changed := false

	serial := e.cfg.Identity.Serial
	if serial == "" {
		if e.st.Serial != "" && !e.st.Stale(e.configHash) {
			serial = e.st.Serial
		} else {
			generated, err := e.cat.GenerateSerial(e.cfg.Model)
			if err != nil {
				return cmdline.Identity{}, false, fmt.Errorf("generate serial: %w", err)
			}
			serial = generated
			changed = true
		}
	}

	var macs []string
	if len(e.cfg.Identity.MACs) > 0 {
		for _, m := range e.cfg.Identity.MACs {
			norm, err := catalog.NormalizeMAC(m)
			if err != nil {
				return cmdline.Identity{}, false, err
			}
			macs = append(macs, norm)
		}
	} else if len(e.st.MACs) == e.cfg.Identity.NICCount && !e.st.Stale(e.configHash) {
		macs = e.st.MACs
	} else {
		generated, err := e.cat.GenerateMACs(e.cfg.Model, e.cfg.Identity.NICCount)
		if err != nil {
			return cmdline.Identity{}, false, fmt.Errorf("generate MACs: %w", err)
		}
		macs = generated
		changed = true
	}

	return cmdline.Identity{Serial: serial, MACs: macs}, changed, nil
}

func (e *env) buildCmdline(id cmdline.Identity) (*cmdline.Builder, error) {
	opts := cmdline.DefaultOptions()
	return cmdline.Build(e.cfg, e.plat, id, opts)
}

// patCacheName is the file name a .pat is cached under. Including the model and
// version keeps several builds side by side in one cache.
//
// patCacheName - .pat 을 캐시에 저장할 파일 이름. 모델과 버전을 넣어 두어
// 여러 빌드가 한 캐시에 나란히 있을 수 있다.
func (e *env) patCacheName() string {
	return fmt.Sprintf("%s-%s.pat", e.cfg.Model, e.cfg.DSM.Version)
}

func (e *env) patCachePath() string {
	return filepath.Join(e.cfg.Paths.Cache, e.patCacheName())
}

// ---------------------------------------------------------------------------
// commands
// 커맨드
// ---------------------------------------------------------------------------

func cmdInit(args []string) error {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	path := configFlag(fs)
	force := fs.Bool("f", false, "overwrite an existing config")
	model := fs.String("model", "", "model to start from (default SA6400)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if _, err := os.Stat(*path); err == nil && !*force {
		return fmt.Errorf("%s already exists (use -f to overwrite)", *path)
	}

	cat, err := catalog.Load()
	if err != nil {
		return err
	}

	cfg := config.Default()
	if *model != "" {
		if _, ok := cat.PlatformForModel(*model); !ok {
			return fmt.Errorf("unknown model %q (try `vibeldr models`)", *model)
		}
		cfg.Model = *model
	}

	// The newest DSM differs per model - 7.4.1 for one, still 7.2.2 for
	// another - so Default() leaves dsm.version empty and the release catalog
	// decides it here. A model with no release in the catalog is an error
	// rather than a half-written loader.yaml, so the question is answered now
	// instead of at the first build.
	//
	// 최신 DSM 은 모델마다 다르다 (어느 모델은 7.4.1, 어느 모델은 아직
	// 7.2.2). 그래서 Default() 는 dsm.version 을 비워 두고 여기서 릴리스
	// 카탈로그가 정한다. 카탈로그에 릴리스가 없는 모델은 미완성 loader.yaml
	// 을 남기지 않고 오류로 끝내, "왜 안 되지" 를 첫 빌드까지 미루지 않는다.
	if versions := cat.KnownVersions(cfg.Model); len(versions) > 0 {
		cfg.DSM.Version = versions[0]
		if rel, ok := cat.ReleaseFor(cfg.Model, versions[0]); ok {
			cfg.DSM.URL = rel.URL
			cfg.DSM.MD5 = rel.MD5
		}
	} else {
		return fmt.Errorf("model %q has no release in the catalog. "+
			"check the supported versions with `vibeldr versions %s`, or "+
			"open loader.yaml and fill in dsm.version by hand",
			cfg.Model, cfg.Model)
	}

	if err := cfg.Save(*path); err != nil {
		return err
	}
	ui.OK("wrote %s", *path)
	ui.Info("model %s, DSM %s", cfg.Model, cfg.DSM.Version)
	if cfg.DSM.URL == "" || cfg.DSM.MD5 == "" {
		ui.Warn("dsm.url / dsm.md5 are empty — not in the catalog yet. Set them with `vibeldr fetch --url ...` or edit loader.yaml")
	}
	ui.Info("next: %s", ui.Cyan("vibeldr validate"))
	return nil
}

func cmdValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ExitOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}

	ui.Section("Configuration")
	ui.Field("File", *path)
	ui.Field("Model", e.cfg.Model)
	ui.Field("Platform", fmt.Sprintf("%s (device tree: %v)", e.plat.Name, e.plat.DT))
	ui.Field("DSM", e.cfg.DSM.Version)
	ui.Field("Kernel", e.plat.Kernels[e.cfg.ProductVersion()])
	fmt.Println()
	ui.OK("configuration is valid")

	if len(e.plat.Flags) > 0 {
		ui.Warn("platform %s requires CPU feature(s): %s - verify the target CPU has them",
			e.plat.Name, strings.Join(e.plat.Flags, ", "))
	}
	return nil
}

func cmdPlatforms(args []string) error {
	fs := flag.NewFlagSet("platforms", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cat, err := catalog.Load()
	if err != nil {
		return err
	}

	ui.Section("Platforms")
	fmt.Printf("  %-16s %-4s %-10s %-8s %s\n", "NAME", "DT", "KERNEL", "MODELS", "CPU FLAGS")
	for _, name := range cat.PlatformNames() {
		p, _ := cat.Platform(name)
		kernels := map[string]bool{}
		for _, k := range p.Kernels {
			kernels[k] = true
		}
		list := make([]string, 0, len(kernels))
		for k := range kernels {
			list = append(list, k)
		}
		sort.Strings(list)

		dt := "-"
		if p.DT {
			dt = "yes"
		}
		fmt.Printf("  %-16s %-4s %-10s %-8d %s\n",
			name, dt, strings.Join(list, ","), len(p.Models), strings.Join(p.Flags, ","))
	}
	return nil
}

func cmdModels(args []string) error {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	platform := fs.String("p", "", "only show models of this platform")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cat, err := catalog.Load()
	if err != nil {
		return err
	}

	ui.Section("Models")
	for _, name := range cat.PlatformNames() {
		if *platform != "" && !strings.EqualFold(name, *platform) {
			continue
		}
		p, _ := cat.Platform(name)
		fmt.Printf("  %s\n", ui.Bold(name))
		for _, m := range p.Models {
			marker := " "
			if _, ok := cat.SerialRule(m); ok {
				// A model with a known serial rule can produce a serial that
				// unlocks the model-locked DSM features.
				//
				// 시리얼 규칙이 알려진 모델은 모델 잠금이 걸린 DSM 기능을 여는
				// 시리얼을 만들 수 있다.
				marker = "*"
			}
			fmt.Printf("    %s %s\n", marker, m)
		}
	}
	fmt.Println()
	ui.Info("%s = a serial number rule is known for this model", ui.Bold("*"))
	return nil
}

func cmdVersions(args []string) error {
	fs := flag.NewFlagSet("versions", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: vibeldr versions <MODEL>")
	}
	model := fs.Arg(0)

	cat, err := catalog.Load()
	if err != nil {
		return err
	}
	plat, ok := cat.PlatformForModel(model)
	if !ok {
		return fmt.Errorf("unknown model %q (try `vibeldr models`)", model)
	}

	ui.Section(fmt.Sprintf("DSM versions for %s", model))
	ui.Field("Platform", plat.Name)
	fmt.Println()
	for _, v := range cat.ProductVersions(plat.Name) {
		fmt.Printf("    DSM %-6s kernel %s\n", v, plat.Kernels[v])
	}
	fmt.Println()
	ui.Info("set the exact build in loader.yaml, e.g. %s", ui.Cyan("dsm: { version: \"7.4.1-90080\" }"))
	return nil
}

func cmdIdentity(args []string) error {
	fs := flag.NewFlagSet("identity", flag.ExitOnError)
	path := configFlag(fs)
	regen := fs.Bool("regenerate", false, "discard the pinned identity and make a new one")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}
	if *regen {
		e.st.Serial = ""
		e.st.MACs = nil
	}

	id, changed, err := e.resolveIdentity()
	if err != nil {
		return err
	}

	ui.Section("Identity")
	ui.Field("Serial", id.Serial)
	for i, m := range id.MACs {
		ui.Field(fmt.Sprintf("MAC %d", i+1), formatMAC(m))
	}
	if _, ok := e.cat.SerialRule(e.cfg.Model); !ok {
		ui.Warn("no serial rule is known for %s, so this serial will not unlock model-locked features", e.cfg.Model)
	}

	if changed {
		e.st.Serial = id.Serial
		e.st.MACs = id.MACs
		e.st.Model = e.cfg.Model
		e.st.ConfigHash = e.configHash
		if err := e.st.Save(e.cfg.Paths.Work); err != nil {
			return err
		}
		fmt.Println()
		ui.OK("pinned in %s - rebuilds will reuse it", state.Path(e.cfg.Paths.Work))
	}
	return nil
}

func formatMAC(m string) string {
	if len(m) != 12 {
		return m
	}
	parts := make([]string, 0, 6)
	for i := 0; i < 12; i += 2 {
		parts = append(parts, m[i:i+2])
	}
	return m + "  (" + strings.Join(parts, ":") + ")"
}

func cmdCmdline(args []string) error {
	fs := flag.NewFlagSet("cmdline", flag.ExitOnError)
	path := configFlag(fs)
	oneLine := fs.Bool("raw", false, "print only the command line, for piping")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}
	id, _, err := e.resolveIdentity()
	if err != nil {
		return err
	}
	b, err := e.buildCmdline(id)
	if err != nil {
		return err
	}

	if *oneLine {
		fmt.Println(b.String())
		return nil
	}

	ui.Section("Kernel command line")
	for _, p := range b.Params() {
		if p.Value == "" {
			fmt.Printf("    %s\n", p.Key)
		} else {
			fmt.Printf("    %s=%s\n", p.Key, ui.Cyan(p.Value))
		}
	}
	fmt.Println()
	ui.Field("Rendered length", fmt.Sprintf("%d characters", len(b.String())))

	problems := b.Validate()
	if len(problems) == 0 {
		ui.OK("command line passes validation")
		return nil
	}
	fmt.Println()
	for _, p := range problems {
		ui.Fail("%s: %s", p.Key, p.Message)
	}
	return fmt.Errorf("%d problem(s) in the kernel command line", len(problems))
}

func cmdPlan(args []string) error {
	fs := flag.NewFlagSet("plan", flag.ExitOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}
	id, _, err := e.resolveIdentity()
	if err != nil {
		return err
	}

	hv := hwscan.DetectHypervisorSysfs()

	b, err := e.buildCmdline(id)
	if err != nil {
		return err
	}

	productVer := e.cfg.ProductVersion()

	ui.Section("Target")
	ui.Field("Model", e.cfg.Model)
	ui.Field("Platform", e.plat.Name)
	ui.Field("Device tree", fmt.Sprintf("%v", e.plat.DT))
	ui.Field("DSM", e.cfg.DSM.Version)
	ui.Field("Kernel", e.plat.Kernels[productVer])
	ui.Field("Serial", id.Serial)
	ui.Field("NICs", fmt.Sprintf("%d", len(id.MACs)))
	ui.Field("Hypervisor", string(hv))

	ui.Section("Steps")
	patPath := e.patCachePath()
	if _, err := os.Stat(patPath); err == nil {
		ui.OK("fetch    %s (already cached)", e.patCacheName())
	} else if e.cfg.DSM.URL == "" {
		ui.Warn("fetch    no dsm.url set - add one, or run `vibeldr fetch --url ...`")
	} else {
		ui.Info("fetch    %s", e.cfg.DSM.URL)
		ui.Info("         -> %s", patPath)
	}

	extractDir := filepath.Join(e.cfg.Paths.Work, "dsm")
	ui.Info("extract  %s -> %s", e.patCacheName(), extractDir)
	for _, f := range pat.WantedFiles {
		ui.Info("           %s", f)
	}

	ui.Info("patch    rd.gz  -> initrd-dsm   %s", ui.Dim("(plan only)"))
	ui.Info("patch    zImage -> zImage-dsm   %s", ui.Dim("(plan only)"))
	ui.Info("image    -> %s", filepath.Join(e.cfg.Paths.Output, "loader.img"))

	ui.Section("Kernel command line")
	fmt.Printf("    %s\n", b.String())

	problems := b.Validate()
	fmt.Println()
	if len(problems) == 0 {
		ui.OK("plan is consistent")
	} else {
		for _, p := range problems {
			ui.Fail("%s: %s", p.Key, p.Message)
		}
		return fmt.Errorf("%d problem(s) found", len(problems))
	}

	if e.st.Stale(e.configHash) {
		ui.Warn("state.json was produced from a different loader.yaml; the next build will redo every step")
	}
	return nil
}

func cmdFetch(args []string) error {
	fs := flag.NewFlagSet("fetch", flag.ExitOnError)
	path := configFlag(fs)
	url := fs.String("url", "", "override dsm.url")
	md5sum := fs.String("md5", "", "override dsm.md5")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}

	srcURL := e.cfg.DSM.URL
	if *url != "" {
		srcURL = *url
	}
	if srcURL == "" {
		return errors.New("no download URL: set dsm.url in loader.yaml or pass --url\n" +
			"  Synology publishes these at https://archive.synology.com/download/Os/DSM")
	}
	wantMD5 := e.cfg.DSM.MD5
	if *md5sum != "" {
		wantMD5 = *md5sum
	}

	// Ctrl+C leaves the .part file in place so the next run resumes.
	// Ctrl+C 로 끊어도 .part 파일이 남아 다음 실행이 이어서 받는다.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	dest := e.patCachePath()
	ui.Section("Fetch")
	ui.Field("URL", srcURL)
	ui.Field("Destination", dest)
	if wantMD5 == "" || wantMD5 == pat.EmptyMD5 {
		ui.Warn("no md5 configured - the download will not be content-verified")
	}
	fmt.Println()

	f := pat.NewFetcher()
	f.OnProgress = func(done, total int64) { ui.ProgressBar("downloading", done, total) }

	if err := f.Fetch(ctx, srcURL, dest, wantMD5); err != nil {
		ui.ClearLine()
		return err
	}
	ui.ClearLine()

	st, err := os.Stat(dest)
	if err != nil {
		return err
	}
	ui.OK("%s (%s)", filepath.Base(dest), ui.Bytes(st.Size()))

	e.st.Stage = state.StageFetched
	e.st.ConfigHash = e.configHash
	e.st.Model = e.cfg.Model
	e.st.Platform = e.plat.Name
	e.st.DSMVersion = e.cfg.DSM.Version
	e.st.PatPath = dest
	if wantMD5 != "" {
		e.st.PatMD5 = wantMD5
	}
	if err := e.st.Save(e.cfg.Paths.Work); err != nil {
		return err
	}

	// The cache file is kept - vibeldr-boot reuses it as it is. But if the format
	// turns out to be encrypted at this point, a later `vibeldr extract` can do
	// nothing with it, so that is said now.
	//
	// 캐시 파일은 남긴다 (vibeldr-boot 가 그대로 재사용한다). 다만 이 시점에서
	// 포맷이 암호화된 것이면 이후 `vibeldr extract` 는 아무것도 못 하니
	// 미리 알린다.
	format, ferr := pat.DetectFormat(dest)
	if ferr != nil {
		return nil
	}
	if format == pat.FormatEncrypted {
		fmt.Println()
		ui.Warn("encrypted .pat detected (%s)", filepath.Base(dest))
		ui.Info("vibeldr on Windows stops here.")
		ui.Info("decryption and zImage/rd.gz extraction are done automatically by the vibeldr-boot TUI when the VM boots.")
		return pat.ErrEncrypted
	}
	return nil
}

func cmdExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ExitOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}

	patPath := e.patCachePath()
	if _, err := os.Stat(patPath); err != nil {
		return fmt.Errorf("%s is not in the cache - run `vibeldr fetch` first", e.patCacheName())
	}

	destDir := filepath.Join(e.cfg.Paths.Work, "dsm")
	ui.Section("Extract")
	ui.Field("Archive", patPath)
	ui.Field("Destination", destDir)

	format, err := pat.DetectFormat(patPath)
	if err != nil {
		return err
	}
	ui.Field("Format", format.String())
	fmt.Println()

	// An encrypted .pat is never touched from the Windows CLI. The cache is left as
	// it is, for vibeldr-boot to reuse, and this exits at once. The real decryption
	// and extraction happen inside the VM, in the vibeldr-boot TUI, with the scemd
	// pulled out of a seed DSM.
	//
	// 암호화된 .pat 은 Windows CLI 에서 절대 손대지 않는다. 캐시는 그대로
	// 두고 (vibeldr-boot 가 재사용) 즉시 종료한다. 실제 복호화·추출은 VM 안
	// vibeldr-boot TUI 가 seed DSM 에서 뽑은 scemd 로 처리한다.
	if format == pat.FormatEncrypted {
		ui.Fail("an encrypted .pat cannot be extracted by vibeldr on Windows.")
		ui.Info("the cached file is left in place (%s).", patPath)
		ui.Info("build an image from this loader.yaml and boot the VM; the vibeldr-boot TUI")
		ui.Info("pulls Synology's own extractor (scemd) from a seed DSM and handles it automatically.")
		return pat.ErrEncrypted
	}

	res, err := pat.Extract(patPath, destDir)
	if err != nil {
		return err
	}

	for _, name := range pat.WantedFiles {
		p, ok := res.Files[name]
		if !ok {
			ui.Warn("%-16s missing", name)
			continue
		}
		st, serr := os.Stat(p)
		size := "?"
		if serr == nil {
			size = ui.Bytes(st.Size())
		}
		ui.OK("%-16s %-10s %s", name, size, ui.Dim(res.Hashes[name][:16]+"..."))
	}

	// The VERSION inside the archive is the authoritative build number. Comparing
	// it against the config is what catches a URL that points at a different
	// build than the one configured - a mismatch that otherwise surfaces only
	// much later, after the wrong kernel has been patched.
	//
	// 아카이브 안의 VERSION 이 빌드 번호의 기준이다. 이것을 설정과 대조해야
	// 설정한 것과 다른 빌드를 가리키는 URL 을 잡을 수 있다. 여기서 못 잡으면
	// 엉뚱한 커널을 패치한 한참 뒤에야 드러난다.
	if vpath, ok := res.Files["VERSION"]; ok {
		info, verr := pat.ParseVersionFile(vpath)
		if verr != nil {
			ui.Warn("could not parse VERSION: %v", verr)
		} else {
			fmt.Println()
			ui.Field("Archive reports", info.Full)
			if info.ProductVer != e.cfg.ProductVersion() {
				return fmt.Errorf("archive is DSM %s but loader.yaml asks for %s - fix dsm.url or dsm.version",
					info.ProductVer, e.cfg.ProductVersion())
			}
			if info.Build != e.cfg.BuildNumber() {
				ui.Warn("build number differs: archive %s, config %s", info.Build, e.cfg.BuildNumber())
			}
			e.st.DSMVersion = info.Full
		}
	}

	e.st.Stage = state.StageExtracted
	e.st.ConfigHash = e.configHash
	e.st.ZImageSHA256 = res.Hashes["zImage"]
	e.st.RamdiskSHA256 = res.Hashes["rd.gz"]
	return e.st.Save(e.cfg.Paths.Work)
}

// patchedRamdisk and patchedZImage are both the file names the patched results
// are saved under and the names GRUB loads them by.
//
// patchedRamdisk / patchedZImage 는 패치된 결과물이 저장되는 파일명이자
// GRUB 이 로드할 때 부르는 이름이다.
const (
	patchedRamdisk = "initrd-dsm"
	patchedZImage  = "zImage-dsm"
)

// cmdPatch is the one place that touches the zImage and rd.gz Synology
// distributed.
//
// The ramdisk: one static helper binary is added and a few hook lines go into
// the boot script. The helper rewrites the PCI addresses in the model's device
// tree to this machine's real ones, loads the drivers the ramdisk did not bring
// along, and handles the pivot.
//
// The zImage: a few bytes inside vmlinux are changed - the boot parameter
// lock, so module.sig_enforce=0 on the command line takes effect, and the
// failure branch of the ramdisk signature check. Unsigned kernel modules can
// then be loaded and our driver pack works. The offsets are not hard-coded but
// found by analysing each vmlinux, so kernel 4.4 and 5.10 are handled alike.
// The original zImage is untouched; the result is saved as a new file beside
// it.
//
// cmdPatch 는 시놀로지가 배포한 zImage 와 rd.gz 에 우리 손을 대는 유일한
// 자리다.
//
// 램디스크: 정적 헬퍼 바이너리 하나가 추가되고 부팅 스크립트에 훅 몇 줄이
// 들어간다. 헬퍼는 모델의 device tree 안 PCI 주소를 이 머신 실제 주소로
// 바꾸고, 램디스크가 실어오지 않은 드라이버를 로드하고, pivot 을 처리한다.
//
// zImage: vmlinux 안의 몇 바이트만 바꾼다. 부트 파라미터 잠금을 풀어
// cmdline 의 module.sig_enforce=0 이 먹히게 하고, 램디스크 서명 검증의 실패
// 분기를 넘긴다. 서명 없는 커널 모듈을 로드할 수 있게 되어 우리 드라이버
// 팩이 통한다. 오프셋은 하드코딩이 아니라 매 vmlinux 를 분석해서 찾으므로
// kernel 4.4 / 5.10 을 가리지 않는다. 원본 zImage 는 손대지 않고 옆에 새
// 파일로 저장된다.
func cmdPatch(args []string) error {
	fs := flag.NewFlagSet("patch", flag.ExitOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	e, err := load(*path)
	if err != nil {
		return err
	}

	dsmDir := filepath.Join(e.cfg.Paths.Work, "dsm")
	src := filepath.Join(dsmDir, "rd.gz")
	raw, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("%s is not there - run `vibeldr extract` first", src)
	}
	helper, err := initbin.Binary()
	if err != nil {
		return err
	}

	ui.Section("Patch")
	ui.Field("Ramdisk", src)
	ui.Field("Helper", fmt.Sprintf("/%s  %s  linux/amd64, static", ramdisk.InitName, ui.Bytes(int64(len(helper)))))
	fmt.Println()

	// Settings that belong to the installed system rather than to the ramdisk.
	// DSM lays its own copy down from the .pat, so these travel inside the
	// ramdisk and are applied on the other side of the pivot.
	//
	// 램디스크가 아니라 설치된 시스템에 속하는 설정들. DSM 은 .pat 에서 자기
	// 사본을 새로 깔기 때문에, 이 설정은 램디스크 안에 실려 가서 pivot 이후에
	// 적용된다.
	var extra []ramdisk.File
	if settings := dsmconf.RenderList(dsmconf.Settings(e.plat.Synoinfo, e.cfg.Synoinfo)); len(settings) > 0 {
		extra = append(extra, ramdisk.File{Name: ramdisk.SynoinfoName, Mode: 0o644, Data: settings})
	}
	// A bay order set by hand is sent along in the ramdisk. The init early in the
	// boot reads that file and uses it instead of the detection order.
	//
	// 베이 순서를 직접 정했으면 램디스크에 실어 보낸다. 부팅 초기의 init 이
	// 이 파일을 읽어 감지 순서 대신 쓴다.
	if plan := renderBayPlan(e.cfg.Storage.BayOrder); len(plan) > 0 {
		extra = append(extra, ramdisk.File{Name: "vibeldr-bay-plan", Mode: 0o644, Data: plan})
	}
	// A network card order set in loader.yaml goes along the same way
	// (hwscan/nicplan.go).
	//
	// loader.yaml 에 정한 랜카드 순서도 같은 식으로 실어 보낸다
	// (hwscan/nicplan.go).
	if plan := hwscan.RenderNICPlan(e.cfg.Identity.NICOrder); len(plan) > 0 {
		extra = append(extra, ramdisk.File{Name: hwscan.NICPlanName, Mode: 0o644, Data: plan})
	}
	// The release this build installs, for the web installer's online install
	// (dsmconf/rss.go).
	//
	// 이 빌드가 설치하는 릴리스. 웹 설치기의 온라인 설치용이다 (dsmconf/rss.go).
	if rel := dsmconf.RenderRelease(e.cfg.DSM.URL, e.cfg.DSM.MD5); rel != nil {
		extra = append(extra, ramdisk.File{Name: dsmconf.ReleaseName, Mode: 0o644, Data: rel})
	}
	// The boot-event notification settings (cmd/vibeldr-init/notify.go).
	// 부팅 이벤트 알림 설정 (cmd/vibeldr-init/notify.go).
	if n := e.cfg.Notify.JSON(); n != nil {
		extra = append(extra, ramdisk.File{Name: ramdisk.NotifyName, Mode: 0o600, Data: n})
	}

	out, rep, err := ramdisk.Patch(raw, helper, extra...)
	if err != nil {
		return err
	}

	dst := filepath.Join(dsmDir, patchedRamdisk)
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return err
	}

	ui.OK("read %d files (%s compressed, %s unpacked)", rep.Entries,
		ui.Bytes(int64(len(raw))), ui.Bytes(int64(rep.Decompressed)))
	ui.OK("added /%s", ramdisk.InitName)
	if n := len(dsmconf.Settings(e.plat.Synoinfo, e.cfg.Synoinfo)); n > 0 {
		ui.OK("carrying %d setting(s) for the installed system", n)
	}
	// The same string the loader prints on every boot, so a boot log can be
	// matched to a build without anyone having to remember what was uploaded.
	//
	// 로더가 부팅할 때마다 찍는 것과 같은 문자열이다. 그래서 무엇을 올렸는지
	// 기억하지 않아도 부팅 로그와 빌드를 맞춰 볼 수 있다.
	ui.OK("build %s - the boot log will say `vibeldr: build %s`", rep.BuildID, rep.BuildID)
	for _, h := range rep.Hooks {
		ui.OK("hooked into %s at line %d", h.File, h.Line)
	}
	for _, s := range rep.Stock {
		ui.OK("edited DSM's %s", s)
	}
	for _, s := range rep.StockMissing {
		ui.Warn("left DSM's %s alone: its line was not found", s)
	}
	ui.OK("wrote %s (%s)", dst, ui.Bytes(int64(len(out))))
	fmt.Println()
	// The jump in size is intended, not a mistake. The kernel takes an
	// uncompressed cpio as an initramfs just as happily.
	//
	// 크기가 확 커지는 건 실수가 아니고 의도된 것이다. 커널은 압축 없는 cpio 도
	// 그대로 initramfs 로 받아들인다.
	ui.Info("repacked without compression - the kernel unpacks a plain cpio initramfs")

	// The zImage is patched too. A failure is logged but does not fail the whole
	// pipeline: unless our driver pack has to load, the original zImage still
	// boots.
	//
	// zImage 도 패치한다. 실패는 로그로 남기되 파이프라인 전체를 실패시키지는
	// 않는다. 우리 드라이버 팩이 로드되어야 하는 경우가 아니라면 zImage 원본
	// 그대로도 부팅은 되기 때문이다.
	if err := patchZImage(dsmDir); err != nil {
		ui.Warn("zImage patch skipped: %v", err)
		ui.Info("in this state unsigned kernel modules are rejected with EKEYREJECTED")
	} else {
		ui.Info("next: %s", ui.Cyan("vibeldr image --donor <loader.img>"))
	}

	e.st.Stage = state.StagePatched
	e.st.ConfigHash = e.configHash
	return e.st.Save(e.cfg.Paths.Work)
}

// patchZImage reads the original zImage, analyses and patches the vmlinux
// inside it, and saves the new bzImage as zImage-dsm.
//
// The sites come from kpatch.Analyze alone. The identity is not part of the
// patch: the serial and the MAC addresses reach the kernel on the command line.
//
// patchZImage 는 원본 zImage 를 읽어 vmlinux 를 분석/패치하고 새 bzImage 를
// zImage-dsm 으로 저장한다.
//
// 패치 사이트는 kpatch.Analyze 결과만으로 정해진다. identity 는 패치에 들어가지
// 않는다. 시리얼과 MAC 은 cmdline 으로 커널에 들어간다.
func patchZImage(dsmDir string) error {
	srcPath := filepath.Join(dsmDir, "zImage")
	dstPath := filepath.Join(dsmDir, patchedZImage)

	raw, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("read %s: %w", srcPath, err)
	}
	bz, err := kpatch.ParseBzImage(raw)
	if err != nil {
		return err
	}
	vmData, err := bz.ExtractVMLinux()
	if err != nil {
		return fmt.Errorf("decompress payload: %w", err)
	}
	v, err := kpatch.ParseVMLinux(vmData)
	if err != nil {
		return fmt.Errorf("parse vmlinux: %w", err)
	}

	fmt.Println()
	ui.Section("Kernel patch")
	ui.Field("Source", srcPath)
	ui.Field("vmlinux", fmt.Sprintf("%s ELF", ui.Bytes(int64(len(vmData)))))

	findings, err := kpatch.Analyze(v)
	if err != nil {
		return fmt.Errorf("analyze: %w", err)
	}
	if len(findings.Sites) == 0 {
		for _, m := range findings.Missing {
			ui.Warn("  not found: %s", m)
		}
		return fmt.Errorf("no patch sites found")
	}
	res := kpatch.Apply(v, findings.Sites)
	for _, s := range res.Applied {
		ui.OK("  %s: 0x%02x -> 0x%02x @ file 0x%x  (%s)", s.Name, s.Old, s.New, s.FileOff, s.Why)
	}
	for _, s := range res.Skipped {
		ui.Warn("  %s: skipped - %s", s.Site.Name, s.Reason)
	}
	for _, m := range findings.Missing {
		ui.Warn("  not found: %s", m)
	}
	if len(res.Applied) == 0 {
		return fmt.Errorf("sites were found but none could be applied")
	}

	// variant 0 is pb=2, the same as the original; variant 1 is pb=1, for a 4.4
	// kernel that overflows the slot. Rebuild takes the first result that fits.
	//
	// variant 0 은 원본과 같은 pb=2, variant 1 은 pb=1 (칸을 넘치는 4.4 커널용).
	// Rebuild 가 칸에 들어가는 첫 결과를 고른다.
	out, err := bz.Rebuild(v.Bytes, func(b []byte, variant int) ([]byte, error) {
		switch variant {
		case 0:
			return lzma.Encode(b, "9e")
		case 1:
			return lzma.Encode(b, "9-pb1")
		default:
			return nil, fmt.Errorf("no compression parameters left to try (variant %d)", variant)
		}
	})
	if err != nil {
		return fmt.Errorf("rebuild bzImage: %w", err)
	}
	if err := os.WriteFile(dstPath, out, 0o644); err != nil {
		return err
	}
	ui.OK("wrote %s (%s)", dstPath, ui.Bytes(int64(len(out))))
	return nil
}

// grubConfig renders the GRUB menu written onto partition 1.
//
// GRUB finds the payload by filesystem label rather than by disk number, so the
// same image boots wherever it is attached - SATA, USB or NVMe.
//
// grubConfig - 파티션 1 에 쓸 GRUB 메뉴를 렌더한다.
//
// GRUB 은 디스크 번호가 아니라 파일시스템 라벨로 페이로드를 찾는다. 그래서
// 같은 이미지가 SATA, USB, NVMe 어디에 붙어도 그대로 부팅된다.
func grubConfig(cfg *config.Config, kernelCmdline string, defaultEntry string, builder bool) []byte {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	w("# Generated by vibeldr - do not edit; change loader.yaml and rebuild.")
	// Selecting a menu entry by hand needs an interactive terminal, which
	// rules out capturing the boot to a file at the same time. Naming the
	// default lets a diagnostic boot be scripted.
	//
	// 메뉴 항목을 손으로 고르려면 대화형 터미널이 있어야 하고, 그러면 부팅을
	// 동시에 파일로 받아 적을 수 없다. 기본 항목을 이름으로 정해 두면 진단용
	// 부팅을 스크립트로 돌릴 수 있다.
	if defaultEntry == "" {
		defaultEntry = "dsm"
	}
	w("set default=%s", defaultEntry)
	w("set timeout=5")
	w("")
	w("insmod part_msdos")
	w("insmod fat")
	w("insmod search_label")
	w("")
	w("if serial --unit=0 --speed=115200 --word=8 --parity=no --stop=1; then")
	w("  terminal_input --append serial")
	w("  terminal_output --append serial")
	w("fi")
	w("")
	// The identity, kept where it can be changed without rebuilding anything.
	//
	// The serial and the MAC addresses are the two values most likely to need
	// changing after an image is written: a machine moved to different network
	// hardware, a second loader that must not look like the first, a MAC the
	// network was already set up around. Rebuilding the image for that is a
	// long way round, so they are GRUB variables with the built values as
	// defaults, and identity.cfg on this partition overrides them.
	//
	// That partition is FAT, which every operating system can mount, so the
	// file can be edited from the machine itself, from another computer, or
	// from the GRUB command line before the boot even starts.
	//
	// The checks use search's own result rather than `[ -f ]`: the BIOS GRUB
	// core has no `test` command, and `[` fails there with "can't find
	// command". search --set changes only the variable it names.
	//
	// identity 는 아무것도 다시 빌드하지 않고 바꿀 수 있는 곳에 둔다.
	//
	// 이미지를 쓴 뒤 바꿀 일이 가장 많은 값이 시리얼과 MAC 주소다. 기계를 다른
	// 네트워크 장비로 옮겼거나, 두 번째 로더가 첫 번째와 같아 보이면 안 되거나,
	// 네트워크가 이미 특정 MAC 에 맞춰 설정돼 있는 경우다. 그때마다 이미지를
	// 다시 빌드하는 건 먼 길이라, 빌드한 값을 기본으로 하는 GRUB 변수로 두고
	// 이 파티션의 identity.cfg 가 덮어쓰게 한다.
	//
	// 이 파티션은 어느 운영체제든 마운트할 수 있는 FAT 이라, 그 기계에서든 다른
	// 컴퓨터에서든, 부팅이 시작되기 전 GRUB 명령줄에서든 파일을 고칠 수 있다.
	//
	// 검사는 `[ -f ]` 가 아니라 search 자신의 결과를 쓴다. BIOS GRUB core 에는
	// `test` 명령이 없어 `[` 가 "can't find command" 로 실패한다. search --set
	// 은 지정한 변수만 바꾼다.
	for _, kv := range identityVars(kernelCmdline) {
		w("set %s=%s", kv[0], kv[1])
	}
	w("if search --set=vibeldr_id --file /%s --no-floppy; then", identityFile)
	w("  source ($vibeldr_id)/%s", identityFile)
	w("fi")
	w("")

	w("# The payload partition carries the patched kernel and ramdisk.")
	w("# Fall back to finding the kernel itself, so a missing or unreadable")
	w("# volume label cannot leave root pointing at the boot partition.")
	w("if search --set=root --label %s --no-floppy; then", imageLabels[2])
	w("  set vibeldr_payload=label")
	w("else")
	w("  search --set=root --file /zImage-dsm --no-floppy")
	w("fi")
	w("")
	kernelCmdline = withIdentityVars(kernelCmdline)

	dsmInitrd := "/initrd-dsm"

	w("menuentry 'DSM %s %s' --id dsm {", cfg.Model, cfg.DSM.Version)
	w("  echo 'Loading DSM kernel...'")
	w("  linux /zImage-dsm %s", kernelCmdline)
	w("  echo 'Loading DSM ramdisk...'")
	w("  initrd %s", dsmInitrd)
	// The Synology kernel writes nothing to the screen (VGA), so from the moment it
	// takes over the display looks stuck and black at 'Booting the kernel.'. These
	// lines are left on the GRUB screen right before the handover so it is not
	// mistaken for a fault; they stay visible until the kernel clears the screen.
	// The GRUB console font has no Korean glyphs and Korean comes out as ??, so
	// they are written in English.
	//
	// 시놀로지 커널은 화면(VGA)에 아무것도 못 쓴다. 그래서 커널로 넘어가는
	// 순간부터 화면이 'Booting the kernel.' 에서 검게 멈춘 것처럼 보인다.
	// 사용자가 고장으로 오해하지 않도록, 넘어가기 직전 GRUB 화면에 안내를
	// 남긴다. 이 줄들은 커널이 화면을 지우기 전까지 그대로 남아 보인다.
	// GRUB 콘솔 폰트에 한글 글리프가 없어 한글은 ?? 로 깨진다. 영어로 쓴다.
	w("  echo ''")
	w("  echo '=================================================================='")
	w("  echo ' Booting DSM now. The screen stays black from here - this is'")
	w("  echo ' normal. Synology kernels have no video console; DSM runs over'")
	w("  echo ' the network. From another PC on the same network, open:'")
	w("  echo '        http://find.synology.com'")
	w("  echo ' (or connect to this box IP on port 5000) to finish install.'")
	w("  echo '=================================================================='")
	w("}")
	w("")
	// The loader's own environment: same kernel, a ramdisk holding one
	// program. This is where the machine is configured and where a new
	// loader is built, so it comes before the reinstall entry - it is the
	// one somebody goes looking for.
	//
	// 로더 자체 환경: 같은 커널에 프로그램 하나만 든 램디스크. 기계를 설정하고
	// 새 로더를 빌드하는 곳이라 재설치 항목보다 앞에 둔다. 사람들이 찾아오는
	// 항목이 이것이다.
	if builder {
		w("menuentry 'vibeldr - configure and build' --id build {")
		w("  echo 'Loading kernel...'")
		// The same command line the DSM entry uses. It is the same kernel,
		// and a Synology kernel given none of its own parameters stops before
		// it can say why - root= and the rest are simply ignored once an
		// initramfs provides /init.
		//
		// DSM 항목과 같은 커맨드라인을 쓴다. 같은 커널이고, 시놀로지 커널은
		// 자기 파라미터를 하나도 못 받으면 이유도 말하기 전에 멈춘다. initramfs
		// 가 /init 을 주면 root= 같은 나머지는 그냥 무시된다.
		w("  linux /zImage-dsm %s", kernelCmdline)
		w("  echo 'Loading vibeldr...'")
		w("  initrd /%s", builderRamdisk)
		w("}")
		w("")
	}

	w("menuentry 'DSM %s %s (reinstall)' --id junior {", cfg.Model, cfg.DSM.Version)
	w("  echo 'Loading DSM kernel...'")
	w("  linux /zImage-dsm %s force_junior", kernelCmdline)
	w("  echo 'Loading DSM ramdisk...'")
	w("  initrd %s", dsmInitrd)
	w("}")
	w("")

	return []byte(b.String())
}

// The GRUB menu entry ids, used both for naming the entries and for the default
// selection.
//
// GRUB 메뉴 엔트리 id. 엔트리 이름 부여와 기본 선택 양쪽에 쓴다.
const (
	entryNormal = "dsm"
)

// imageLabels are the FAT volume labels, in partition order.
// imageLabels - FAT 볼륨 라벨, 파티션 순서대로.
var imageLabels = [3]string{"VIBELDR1", "VIBELDR2", "VIBELDR3"}

func cmdImage(args []string) error {
	fs := flag.NewFlagSet("image", flag.ExitOnError)
	path := configFlag(fs)
	out := fs.String("o", "", "output path (default <paths.output>/loader.img)")
	sizeMB := fs.Int("size", 1024, "total image size in MiB")
	p1MB := fs.Int("p1", 128, "partition 1 size in MiB (GRUB and config)")
	p2MB := fs.Int("p2", 128, "partition 2 size in MiB (untouched DSM originals)")
	donor := fs.String("donor", "", "existing loader .img to take the MBR boot code and GRUB core image from")
	// A diagnostic boot that shows what really needs patching: Synology's own
	// kernel and ramdisk, unmodified, with this loader's command line. Whatever
	// fails there is the real work list.
	//
	// 무엇을 정말 패치해야 하는지 보여 주는 진단용 부팅. 시놀로지의 커널과
	// 램디스크를 손대지 않은 채 이 로더의 커맨드라인으로 부팅한다. 거기서
	// 실패하는 것이 실제 할 일 목록이다.
	passthrough := fs.Bool("passthrough", false, "boot the unpatched DSM kernel and ramdisk (diagnostic)")
	// Which entry boots without anyone at the keyboard. A machine with a
	// serial console and no screen is the normal case here, so the default
	// has to be chosen at build time rather than picked from a menu.
	//
	// 키보드 앞에 아무도 없을 때 부팅할 항목. 화면 없이 시리얼 콘솔만 있는
	// 기계가 흔한 경우라, 메뉴에서 고르는 게 아니라 빌드할 때 기본값을 정해야
	// 한다.
	boot := fs.String("boot", entryNormal, "menu entry to boot by default: dsm | build | junior")
	// Drivers for hardware Synology never shipped one for.
	//
	// They go on the loader's own partition rather than into the ramdisk, and
	// the difference matters more than it looks. A ramdisk is unpacked into
	// memory and stays there for the life of the machine, so every driver put
	// in it is paid for whether or not the card exists; a partition is read
	// only when something asks. Several hundred drivers on disk cost nothing
	// until one of them turns out to be the one this machine needs.
	//
	// 시놀로지가 드라이버를 내놓은 적 없는 하드웨어용 드라이버.
	//
	// 램디스크가 아니라 로더 자체 파티션에 두는데, 이 차이는 보기보다 크다.
	// 램디스크는 메모리에 풀려 기계가 꺼질 때까지 남으므로, 거기 넣은
	// 드라이버는 카드가 있든 없든 전부 비용이 든다. 파티션은 누가 요청할 때만
	// 읽힌다. 디스크에 드라이버 수백 개가 있어도, 그중 하나가 이 기계에 필요한
	// 것으로 밝혀지기 전까지는 비용이 없다.
	modulesDir := fs.String("modules", "", "directory of .ko files to carry on the loader partition")
	noModules := fs.Bool("no-modules", false, "build without the driver partition")
	// Accepted and ignored, so a command line that passes it still works: the
	// DSM kernels of these models have no early microcode loader.
	//
	// 이 플래그를 넘기는 명령줄도 돌도록 받기만 하고 무시한다. 이 모델들의 DSM
	// 커널에는 조기 마이크로코드 로더가 없다.
	fs.Bool("microcode", false, "no effect; kept so older command lines still work")
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}
	id, _, err := e.resolveIdentity()
	if err != nil {
		return err
	}
	b, err := e.buildCmdline(id)
	if err != nil {
		return err
	}
	if problems := b.Validate(); len(problems) != 0 {
		for _, p := range problems {
			ui.Fail("%s: %s", p.Key, p.Message)
		}
		return fmt.Errorf("refusing to build an image from an invalid command line")
	}

	outPath := *out
	if outPath == "" {
		outPath = filepath.Join(e.cfg.Paths.Output, "loader.img")
	}

	// The loader's own environment, if one has been built: the same kernel
	// with a ramdisk holding a single program. Booting it is how the machine
	// configures and rebuilds itself instead of being handed a finished image.
	//
	// 로더 자체 환경이 빌드돼 있으면 싣는다. 같은 커널에 프로그램 하나만 든
	// 램디스크다. 이것으로 부팅하면 완성된 이미지를 받는 대신 기계가 스스로
	// 설정하고 다시 빌드한다.
	bootRamdisk := filepath.Join(e.cfg.Paths.Work, "dsm", builderRamdisk)
	_, err = os.Stat(bootRamdisk)
	haveBuilder := err == nil

	// Partition 1: what the loader needs to boot and to remember its config.
	// 파티션 1: 로더가 부팅하고 설정을 기억하는 데 필요한 것.
	content := &image.Content{
		P1: []image.File{
			{Path: "boot/grub/grub.cfg", Data: grubConfig(e.cfg, b.String(), *boot, haveBuilder)},
			{Path: "loader.yaml", Source: e.configPath},
			{Path: "cmdline.txt", Data: []byte(b.String() + "\n")},
			// Editable without rebuilding anything: GRUB reads it before the
			// kernel starts, and this partition is FAT so any machine can
			// mount it.
			//
			// 다시 빌드하지 않고 고칠 수 있다. GRUB 이 커널 시작 전에 읽고, 이
			// 파티션은 FAT 이라 어느 기계에서든 마운트할 수 있다.
			{Path: identityFile, Data: identityTemplate(b.String())},
		},
	}

	// Partition 2: the untouched DSM originals, when they have been extracted.
	// 파티션 2: 추출돼 있으면 손대지 않은 DSM 원본.
	dsmDir := filepath.Join(e.cfg.Paths.Work, "dsm")
	var haveDSM int
	for _, name := range pat.WantedFiles {
		src := filepath.Join(dsmDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		content.P2 = append(content.P2, image.File{Path: name, Source: src})
		haveDSM++
	}

	// Partition 3: the patched kernel and ramdisk by default. In passthrough mode
	// the originals go on unchanged, so GRUB boots exactly what Synology shipped.
	//
	// 파티션 3: 패치된 커널과 램디스크가 기본이다. passthrough 모드면 원본
	// 그대로 실어서 GRUB 이 시놀로지가 보낸 것 그대로 부팅한다.
	zImage := filepath.Join(dsmDir, patchedZImage)
	if _, err := os.Stat(zImage); err != nil {
		// With no patched zImage yet, fall back to the original. It still boots; only
		// unsigned modules cannot be loaded.
		//
		// 패치된 zImage 가 아직 없으면 원본으로 폴백한다 (부팅은 되고, 서명 없는
		// 모듈만 못 로드한다).
		zImage = filepath.Join(dsmDir, "zImage")
	}
	ramdiskSrc := filepath.Join(dsmDir, patchedRamdisk)
	needed := "`vibeldr patch`"
	if *passthrough {
		zImage = filepath.Join(dsmDir, "zImage")
		ramdiskSrc = filepath.Join(dsmDir, "rd.gz")
		needed = "`vibeldr extract`"
	}
	for _, f := range []string{zImage, ramdiskSrc} {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("%s is missing; run %s first", filepath.Base(f), needed)
		}
	}
	// Whichever sources were picked above, they go on partition 3 under the
	// same two names, zImage-dsm and initrd-dsm, so the GRUB menu is the same
	// in every mode.
	//
	// 위에서 어느 원본을 골랐든 파티션 3 에는 zImage-dsm, initrd-dsm 이라는 같은
	// 두 이름으로 들어간다. 그래서 모드와 상관없이 GRUB 메뉴가 같다.
	content.P3 = append(content.P3,
		image.File{Path: "zImage-dsm", Source: zImage},
		image.File{Path: patchedRamdisk, Source: ramdiskSrc},
	)
	if haveBuilder {
		content.P3 = append(content.P3, image.File{Path: builderRamdisk, Source: bootRamdisk})
	}
	// The driver pack on partition 4. A directory given explicitly is used as it
	// is; otherwise the pack matching this model's platform and kernel is fetched.
	// A Synology kernel carries drivers only for the hardware they sell, so left
	// alone a machine with another NIC or controller sees neither network nor disk.
	//
	// 파티션 4 의 드라이버 팩. 디렉터리를 직접 주면 그걸 쓰고, 안 주면 이
	// 모델의 플랫폼/커널에 맞는 팩을 받아 온다. 시놀로지 커널은 자기네가
	// 파는 하드웨어의 드라이버만 담고 있어서, 그대로 두면 다른 NIC 이나
	// 컨트롤러를 꽂은 기계가 네트워크도 디스크도 못 잡는다.
	pack, packCount, err := modulePack(*modulesDir)
	if err != nil {
		return err
	}
	if pack == nil && !*noModules {
		name := catalog.ModulePackName(e.plat.Name, e.cfg.Model)
		ui.Info("fetching driver pack (latest release of %s, %s)...", catalog.ModuleRepo, name)
		mods, info, ferr := image.FetchModulePack(context.Background(), e.cfg.Paths.Cache, e.plat.Name, e.cfg.Model)
		if ferr != nil {
			return fmt.Errorf("driver pack: %w\n  (skip it with --no-modules or give one with --modules)", ferr)
		}
		if pack, err = image.ModulePackCPIO(mods); err != nil {
			return fmt.Errorf("driver pack %s: %w", name, err)
		}
		packCount = mods.Modules()
		from := info.Tag
		if info.FromCache {
			from += ", cached"
		}
		ui.OK("%d drivers (%s) → %s", packCount, from, ui.Bytes(int64(len(pack))))
		if info.FirmwareErr != nil {
			ui.Warn("could not fetch the firmware pack: %v - loading drivers only", info.FirmwareErr)
		} else {
			ui.OK("%d firmware files", info.Firmware)
		}
	}

	ui.Section("Image")
	ui.Field("Output", outPath)
	ui.Field("Size", fmt.Sprintf("%d MiB", *sizeMB))
	ui.Field("Layout", fmt.Sprintf("p1 %d MiB / p2 %d MiB / p3 rest (all FAT32)", *p1MB, *p2MB))
	if *donor != "" {
		ui.Field("Donor", *donor)
	}
	fmt.Println()

	builder := &image.Builder{
		TotalMB:   *sizeMB,
		P1MB:      *p1MB,
		P2MB:      *p2MB,
		P4MB:      packPartitionMB(len(pack)),
		P4:        pack,
		Labels:    imageLabels,
		DonorPath: *donor,
	}
	res, err := builder.Build(outPath, content)
	if err != nil {
		return err
	}

	for i, p := range res.Partitions {
		// The fourth partition carries no filesystem and so has no label.
		// 네 번째 파티션은 파일시스템이 없어서 라벨도 없다.
		label := "(raw)"
		if i < len(imageLabels) {
			label = imageLabels[i]
		}
		ui.OK("p%d  %-9s %8s  LBA %d..%d", i+1, label,
			ui.Bytes(p.Bytes()), p.StartLBA, p.End()-1)
	}
	fmt.Println()
	if res.DonatedFiles > 0 {
		ui.OK("copied %d file(s) from the donor's boot partition (GRUB modules)", res.DonatedFiles)
	}
	ui.OK("wrote %s (%s)", outPath, ui.Bytes(res.SizeBytes))
	for _, w := range res.Warnings {
		ui.Warn("%s", w)
	}

	if haveDSM == 0 {
		ui.Warn("partition 2 is empty - run `vibeldr extract` to put the DSM kernel in it")
	}
	// Being explicit here matters: an image that looks complete but cannot boot
	// is worse than one that says so.
	//
	// 여기서 분명히 말하는 게 중요하다. 완성돼 보이는데 부팅이 안 되는 이미지는
	// 안 된다고 말해 주는 이미지보다 나쁘다.
	if *passthrough {
		ui.OK("passthrough: p3 carries Synology's unmodified kernel and ramdisk")
		ui.Info("this is the diagnostic boot - whatever fails is what actually needs patching")
	} else {
		if filepath.Base(zImage) == patchedZImage {
			ui.OK("p3 carries the patched kernel (%s) and the patched ramdisk", patchedZImage)
		} else {
			ui.OK("p3 carries the original kernel and the patched ramdisk")
			ui.Warn("kernel is unpatched - run `vibeldr patch` to enable signed-module bypass")
		}
		if packCount > 0 {
			ui.OK("p4 carries %d driver(s), %s, read without a filesystem", packCount, ui.Bytes(int64(len(pack))))
		}
	}
	if *donor == "" {
		ui.OK("GRUB %s, built into the image - no donor needed", image.GRUBVersion)
	}
	ui.Info("next: attach %s to a VM (any hypervisor; MBR SeaBIOS boot)", ui.Cyan(filepath.Join(e.cfg.Paths.Output, "loader.img")))
	return nil
}

// cmdBootstrap builds an "empty" bootstrap image, one where neither the model
// nor the DSM version has been decided yet.
//
// The sequence:
//  1. get an Alpine LTS kernel into the cache, downloading it only on the first
//     run.
//  2. read the Linux vibeldr-boot binary and wrap it in a cpio initrd holding
//     one init.
//  3. write a four-partition image: the GRUB configuration, the kernel and the
//     initrd on P1 (FAT); P2 as an empty FAT, which DSM mounts as synoboot2; P3
//     as an empty FAT that vibeldr-boot fills with the patched kernel and
//     ramdisk after boot; and P4 for the driver pack, empty unless
//     --driver-pack gives one.
//
// Attaching the resulting image to a VM has GRUB load the generic kernel, and
// vibeldr-boot brings up the TUI for the user to choose a model and a DSM.
// From there vibeldr-boot handles the download, the scemd decryption, the
// patching, the partition rewrite and the reboot.
//
// This command does not touch loader.yaml: a bootstrap image has no values to
// plant. The identity and the network preferences are either decided by
// vibeldr-boot at boot time or asked for in the TUI.
//
// cmdBootstrap - 모델/DSM 이 아직 결정되지 않은 "빈" 부트스트랩 이미지를
// 만든다.
//
// 실행 흐름:
//  1. Alpine LTS 커널을 캐시에 둔다. 처음 실행할 때만 내려받는다.
//  2. 리눅스용 vibeldr-boot 바이너리를 읽어 init 하나만 든 cpio initrd 로
//     감싼다.
//  3. 파티션 네 개짜리 이미지를 쓴다. P1(FAT) 에는 GRUB 설정, 커널, initrd.
//     P2 는 빈 FAT 이고 DSM 이 synoboot2 로 붙인다. P3 는 빈 FAT 이고 부팅
//     뒤 vibeldr-boot 이 패치된 커널과 램디스크로 채운다. P4 는 드라이버 팩
//     자리로, --driver-pack 을 주지 않으면 비어 있다.
//
// 결과 이미지를 VM 에 붙이면 GRUB 이 제네릭 커널을 로드하고, vibeldr-boot
// 이 TUI 를 띄워 사용자가 모델/DSM 을 고른다. 그 다음은 vibeldr-boot 이
// 다운로드/scemd 복호화/patch/파티션 재작성/리부트까지 처리한다.
//
// loader.yaml 은 이 명령이 손대지 않는다. 부트스트랩 이미지에는 심을 값이
// 없다 (identity/네트워크 선호는 vibeldr-boot 이 부팅 시점에 결정하거나
// TUI 에서 물어본다).
func cmdBootstrap(args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	out := fs.String("o", "", "output path (default work/out/bootstrap.img)")
	sizeMB := fs.Int("size", 1024, "total image size in MiB")
	p1MB := fs.Int("p1", 128, "partition 1 size in MiB (GRUB + kernel + initrd)")
	p2MB := fs.Int("p2", 128, "partition 2 size in MiB (synoboot2, left empty)")
	// Partition 4 is created empty. After booting, the TUI fetches the driver pack
	// for the chosen model, adds Synology's originals from the .pat and writes it
	// here. Without it created in advance there is nowhere to write, and changing
	// the partition table mid-boot is far more trouble. The default fits the
	// largest pack, SA6400's (394 MiB with the originals and firmware), and is
	// the most the helper reads from it (packMaxBytes in cmd/vibeldr-init).
	// Partition 3 is left with about 255 MiB, of which an install uses under
	// 50.
	//
	// 파티션 4 는 빈 채로 만들어 둔다. 부팅한 뒤 TUI 가 고른 모델의 드라이버 팩을
	// 받아 .pat 의 시놀 원본을 더해 여기에 쓴다. 미리 만들어 두지 않으면 쓸 자리가
	// 없고, 파티션 표를 부팅 중에 고치는 건 훨씬 성가시다. 기본값은 가장 큰 팩인
	// SA6400 의 것(원본·펌웨어 포함 394 MiB)이 들어가는 크기이고, 헬퍼가 팩에서
	// 읽는 최대치(cmd/vibeldr-init 의 packMaxBytes)와 같다. 파티션 3 에는 약
	// 255 MiB 가 남고, 설치는 그중 50 MiB 도 안 쓴다.
	p4MB := fs.Int("p4", 512, "partition 4 size in MiB (driver pack, filled after boot)")
	cacheDir := fs.String("cache", filepath.Join("work", "cache"), "kernel/apk cache directory")
	bootBin := fs.String("vibeldr-boot", "./vibeldr-boot", "path to vibeldr-boot linux/amd64 binary")
	noGzip := fs.Bool("no-gzip", false, "leave initrd as raw cpio (diagnostic; needs larger p1)")
	// Accepted and ignored, so a command line that passes it still works: the
	// loader does not embed DSM kernels, so the flag has nothing to turn off.
	//
	// 이 플래그를 넘기는 명령줄도 돌도록 받기만 하고 무시한다. 로더는 DSM 커널을
	// 임베드하지 않으므로 이 플래그가 끌 것이 없다.
	fs.Bool("no-embed", true, "no effect; kept so older command lines still work")
	// A driver pack prepared in advance goes onto partition 4 as it is. Nothing has
	// to be fetched over the network during the install, which makes an offline
	// install possible.
	//
	// 미리 만들어 둔 드라이버 팩을 파티션 4 에 그대로 싣는다. 설치 때
	// 네트워크에서 받지 않아도 되므로 오프라인 설치가 가능해진다.
	driverPack := fs.String("driver-pack", "", "driver pack (cpio) to put on partition 4; leave empty to download it at install time")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// If the sources are newer than the binaries, rebuild first.
	//
	// Without this check, code fixed a moment ago is burned into the image without
	// being in it. The burn looks normal and the machine boots, so a long while is
	// spent wondering why the fix has no effect.
	//
	// The ramdisk helper goes first. It is embedded into vibeldr-boot, and that
	// vibeldr-boot is what goes on the image. The other order leaves the old helper
	// in place.
	//
	// 소스가 바이너리보다 새로우면 먼저 다시 빌드한다.
	//
	// 이 확인이 없으면 방금 고친 코드가 이미지에 안 들어간 채로 구워진다.
	// 겉으로는 정상적으로 구워지고 부팅도 되기 때문에, 고친 내용이 왜
	// 반영되지 않는지 한참 헤매게 된다.
	//
	// 램디스크 헬퍼가 먼저다. 이게 vibeldr-boot 안에 embed 되고, 그
	// vibeldr-boot 이 이미지에 실린다. 순서가 뒤집히면 옛 헬퍼가 그대로 간다.
	if err := rebuildInitBinaryIfStale(); err != nil {
		return err
	}
	if err := rebuildBootBinaryIfStale(*bootBin); err != nil {
		return err
	}

	// The vibeldr-boot binary is checked first, so the run does not spend time
	// downloading a kernel only to fail at the very end.
	//
	// vibeldr-boot 바이너리를 먼저 확인해서, 커널 다운로드로 시간 쓴 뒤
	// 마지막에 실패하는 흐름을 피한다.
	bin, err := os.ReadFile(*bootBin)
	if err != nil {
		return fmt.Errorf("cannot open the vibeldr-boot binary (%s): %w\n"+
			"  build it first: GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o vibeldr-boot ./cmd/vibeldr-boot",
			*bootBin, err)
	}
	if len(bin) == 0 {
		return fmt.Errorf("the vibeldr-boot binary is empty (%s)", *bootBin)
	}

	// The only file the kernel opens on this early boot: one init. An uncompressed
	// newc cpio is taken as an initramfs by the kernel directly.
	//
	// 초기 부팅에서 커널이 열 유일한 파일: init 한 개.
	// 압축 없는 newc cpio 는 커널이 initramfs 로 그대로 받아들인다.
	a := ramdisk.NewArchive()
	if err := a.Add("init", ramdisk.ModeRegular|0o755, bin); err != nil {
		return fmt.Errorf("assemble initrd: %w", err)
	}
	initrd, err := a.Bytes()
	if err != nil {
		return fmt.Errorf("serialize initrd: %w", err)
	}

	outPath := *out
	if outPath == "" {
		outPath = filepath.Join("work", "out", "bootstrap.img")
	}

	ui.Section("Bootstrap image")
	ui.Field("Output", outPath)
	ui.Field("Size", fmt.Sprintf("%d MiB (p1 %d / p2 %d / p3 rest)", *sizeMB, *p1MB, *p2MB))
	ui.Field("vibeldr-boot", fmt.Sprintf("%s  %s", *bootBin, ui.Bytes(int64(len(bin)))))
	ui.Field("Kernel cache", *cacheDir)
	fmt.Println()

	ui.Info("fetching the Alpine LTS kernel + virtio/scsi/nvme/net/vfat modules...")
	kernelPath, mods, kver, err := image.FetchGenericKernelAndModules(*cacheDir)
	if err != nil {
		return fmt.Errorf("get generic kernel/modules: %w", err)
	}
	kst, err := os.Stat(kernelPath)
	if err != nil {
		return err
	}
	ui.OK("kernel  %s  %s  (v%s)", filepath.Base(kernelPath), ui.Bytes(kst.Size()), kver)
	var modBytes int
	for _, m := range mods {
		modBytes += len(m.Data)
	}
	ui.OK("modules %d files  %s", len(mods), ui.Bytes(int64(modBytes)))
	// Reassembling the initrd: after init, every module is added at its real path
	// (lib/modules/VER/...).
	//
	// initrd 재조립: init 다음에 모든 모듈을 실제 경로 (lib/modules/VER/...) 로
	// 추가한다.
	a = ramdisk.NewArchive()
	if err := a.Add("init", ramdisk.ModeRegular|0o755, bin); err != nil {
		return fmt.Errorf("reassemble initrd: %w", err)
	}
	for _, m := range mods {
		if err := a.Add(m.Name, ramdisk.ModeRegular|m.Mode, m.Data); err != nil {
			return fmt.Errorf("add module %s: %w", m.Name, err)
		}
	}
	// The boot environment fetches the .pat over HTTPS. With no trust store the
	// certificate check fails and nothing downloads, so a CA bundle goes along.
	//
	// 부트 환경은 .pat 을 HTTPS 로 받는다. 신뢰 저장소가 없으면 인증서
	// 검증이 실패해 다운로드가 안 되므로 CA 번들을 함께 싣는다.
	caPEM, err := image.FetchCABundle(*cacheDir)
	if err != nil {
		return fmt.Errorf("get CA bundle: %w", err)
	}
	if err := a.Add(catalog.AlpineCACertPath, ramdisk.ModeRegular|0o644, caPEM); err != nil {
		return fmt.Errorf("add CA bundle: %w", err)
	}
	ui.OK("CA bundle %s", ui.Bytes(int64(len(caPEM))))

	// From DSM 7.1 the .pat is locked and opening it needs Synology's own binary.
	// The boot environment has not even a libc, so that binary and the libraries it
	// opens at run time are extracted here and carried along.
	//
	// DSM 7.1 부터 .pat 이 잠겨 있어 여는 데 시놀로지 자체 바이너리가
	// 필요하다. 부트 환경에는 libc 조차 없으므로, 그 바이너리와 그것이
	// 실행 중에 여는 라이브러리를 여기서 뽑아 함께 싣는다.
	ui.Info("fetching the Synology extractor (scemd)...")
	scemdFiles, err := image.FetchScemdBundle(*cacheDir)
	if err != nil {
		return fmt.Errorf("get scemd bundle: %w", err)
	}
	scemdNames := make([]string, 0, len(scemdFiles))
	for name := range scemdFiles {
		scemdNames = append(scemdNames, name)
	}
	sort.Strings(scemdNames)
	var scemdBytes int
	for _, name := range scemdNames {
		if err := a.Add(name, ramdisk.ModeRegular|0o755, scemdFiles[name]); err != nil {
			return fmt.Errorf("add scemd bundle %s: %w", name, err)
		}
		scemdBytes += len(scemdFiles[name])
	}
	ui.OK("scemd  %d files  %s", len(scemdNames), ui.Bytes(int64(scemdBytes)))

	// Turning off the DSM kernel's module signature enforcement means changing a
	// few bytes inside the kernel, and those bytes are inside an LZMA blob, so the
	// whole thing has to be recompressed. A result larger than the original slot
	// does not go back in, and since the compressor Synology used was liblzma, the
	// same liblzma is carried along to produce the same size.
	//
	// DSM 커널의 모듈 서명 강제를 끄려면 커널 안의 몇 바이트를 바꿔야 하는데,
	// 그 바이트들이 LZMA 덩어리 안에 있어 통째로 다시 압축해야 한다. 결과가
	// 원본 칸보다 크면 넣을 수 없고, 시놀로지가 쓴 압축기가 liblzma 이므로
	// 같은 liblzma 를 실어 같은 크기를 낸다.
	xzFiles, err := image.FetchXZ(*cacheDir)
	if err != nil {
		return fmt.Errorf("get xz: %w", err)
	}
	xzNames := make([]string, 0, len(xzFiles))
	for name := range xzFiles {
		xzNames = append(xzNames, name)
	}
	sort.Strings(xzNames)
	var xzBytes int
	for _, name := range xzNames {
		if err := a.Add(name, ramdisk.ModeRegular|0o755, xzFiles[name]); err != nil {
			return fmt.Errorf("xz %s add: %w", name, err)
		}
		xzBytes += len(xzFiles[name])
	}
	ui.OK("xz     %d files  %s", len(xzNames), ui.Bytes(int64(xzBytes)))
	initrd, err = a.Bytes()
	if err != nil {
		return fmt.Errorf("reserialize initrd: %w", err)
	}
	// The kernel detects an initramfs cpio by its gzip, xz or lzma signature and
	// decompresses it automatically. A kernel module tree has low text density and
	// gzip gets it to 60-65%, which keeps p1 at its default 128MB. With --no-gzip
	// it stays a raw cpio, for debugging the kernel's parser.
	//
	// 커널은 initramfs cpio 를 gzip/xz/lzma 시그니처로 감지해 자동으로 압축을
	// 푼다. 커널 모듈 트리는 텍스트 밀도가 낮아 gzip 으로 60~65% 로 줄어든다.
	// p1 을 기본값 128MB 로 유지할 수 있다. --no-gzip 이면 raw cpio 로 둔다
	// (커널 파서 문제 디버깅용).
	if !*noGzip {
		var buf bytes.Buffer
		zw, gzErr := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if gzErr != nil {
			return fmt.Errorf("initrd gzip writer: %w", gzErr)
		}
		if _, wErr := zw.Write(initrd); wErr != nil {
			return fmt.Errorf("initrd gzip write: %w", wErr)
		}
		if cErr := zw.Close(); cErr != nil {
			return fmt.Errorf("initrd gzip close: %w", cErr)
		}
		ui.OK("initrd  %s → %s (gzip)", ui.Bytes(int64(len(initrd))), ui.Bytes(int64(buf.Len())))
		initrd = buf.Bytes()
	} else {
		ui.OK("initrd  %s (raw cpio, no compression)", ui.Bytes(int64(len(initrd))))
	}

	content := &image.Content{
		P1: []image.File{
			{Path: "boot/grub/grub.cfg", Data: image.BootstrapGrubConfig()},
			{Path: image.BootstrapKernelName, Source: kernelPath},
			{Path: image.BootstrapInitrdName, Data: initrd},
		},
	}

	var p4 []byte
	if *driverPack != "" {
		b, err := os.ReadFile(*driverPack)
		if err != nil {
			return fmt.Errorf("cannot read the driver pack: %w", err)
		}
		if need := packPartitionMB(len(b)); need > *p4MB {
			return fmt.Errorf("the driver pack is %d MiB but partition 4 is %d MiB - set --p4 to %d or more",
				need, *p4MB, need)
		}
		p4 = b
		ui.OK("putting driver pack %s on P4 (%s)", filepath.Base(*driverPack), ui.Bytes(int64(len(b))))
	}

	builder := &image.Builder{
		TotalMB: *sizeMB,
		P1MB:    *p1MB,
		P2MB:    *p2MB,
		P4MB:    *p4MB,
		P4:      p4,
		Labels:  imageLabels,
	}
	res, err := builder.Build(outPath, content)
	if err != nil {
		return err
	}

	for i, p := range res.Partitions {
		label := "(raw)"
		if i < len(imageLabels) {
			label = imageLabels[i]
		}
		ui.OK("p%d  %-9s %8s  LBA %d..%d", i+1, label,
			ui.Bytes(p.Bytes()), p.StartLBA, p.End()-1)
	}
	fmt.Println()
	ui.OK("wrote %s (%s)", outPath, ui.Bytes(res.SizeBytes))
	for _, w := range res.Warnings {
		ui.Warn("%s", w)
	}
	ui.OK("p1 carries: boot/grub/grub.cfg, %s, %s",
		image.BootstrapKernelName, image.BootstrapInitrdName)
	ui.Info("p2 and p3 are empty FATs: vibeldr-boot fills p3 with the patched kernel and ramdisk after boot")
	ui.Info("next: attach %s to the VM and enter the TUI on the serial console", ui.Cyan(outPath))
	return nil
}

func cmdStatus(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	path := configFlag(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	e, err := load(*path)
	if err != nil {
		return err
	}

	ui.Section("Status")
	ui.Field("Config", e.configPath)
	ui.Field("Work directory", e.cfg.Paths.Work)
	ui.Field("Stage", string(e.st.Stage))
	if e.st.Serial != "" {
		ui.Field("Pinned serial", e.st.Serial)
	}
	if !e.st.UpdatedAt.IsZero() {
		ui.Field("Last updated", e.st.UpdatedAt.Local().Format("2006-01-02 15:04:05"))
	}

	fmt.Println()
	for _, s := range state.Order[1:] {
		mark := ui.Dim("- ")
		if e.st.Stage.AtLeast(s) {
			mark = ui.Green("v ")
		}
		fmt.Printf("  %s%s\n", mark, s)
	}

	if e.st.Stale(e.configHash) {
		fmt.Println()
		ui.Warn("loader.yaml changed since this state was written")
		ui.Info("the next build will start over from fetch")
	}
	return nil
}

// identityFile is the small editable file on the boot partition that can
// override the identity the image was built with.
//
// identityFile - 부트 파티션에 있는 작은 편집용 파일. 이미지를 빌드할 때 넣은
// identity 를 덮어쓸 수 있다.
const identityFile = "identity.cfg"

// identityKeys are the command-line parameters worth being able to change
// without a rebuild: everything that says which machine this is, rather than
// what it is made of.
//
// syno_hw_version is here with a limit worth knowing. Changing it picks a
// different model, and that only works between models built from the same .pat
// - the kernel and the ramdisk in this image belong to one of them. Across
// platforms it produces a machine that does not boot, and the fix is to build
// an image for that model instead.
//
// identityKeys - 다시 빌드하지 않고 바꿀 수 있으면 좋은 커맨드라인
// 파라미터들. 기계가 무엇으로 만들어졌는지가 아니라 어느 기계인지를 말하는
// 값 전부다.
//
// syno_hw_version 에는 알아 둘 한계가 있다. 바꾸면 다른 모델이 되는데, 같은
// .pat 으로 만드는 모델끼리만 통한다. 이 이미지의 커널과 램디스크는 그중 한
// 모델의 것이기 때문이다. 플랫폼을 넘어가면 부팅이 안 되는 기계가 되고, 그땐
// 그 모델용 이미지를 따로 빌드해야 한다.
var identityKeys = []string{
	"syno_hw_version",
	"sn",
	"netif_num",
	"mac1", "mac2", "mac3", "mac4",
}

// identityVars pulls the built-in values out of the rendered command line, so
// that grub.cfg can declare them as defaults.
//
// identityVars - 렌더된 커맨드라인에서 빌드 때 넣은 값을 뽑아낸다. grub.cfg 가
// 이 값들을 기본값으로 선언할 수 있게 하기 위해서다.
func identityVars(cmdline string) [][2]string {
	var out [][2]string
	for _, k := range identityKeys {
		if v, ok := cmdlineValue(cmdline, k); ok {
			out = append(out, [2]string{k, v})
		}
	}
	return out
}

// withIdentityVars replaces those values with references to the variables, so
// that whatever identity.cfg last set is what the kernel is told.
//
// withIdentityVars - 그 값들을 변수 참조로 바꾼다. 그래서 identity.cfg 가
// 마지막으로 정한 값이 커널에 전달된다.
func withIdentityVars(cmdline string) string {
	for _, k := range identityKeys {
		if v, ok := cmdlineValue(cmdline, k); ok {
			cmdline = strings.Replace(cmdline, k+"="+v, k+"=$"+k, 1)
		}
	}
	return cmdline
}

func cmdlineValue(cmdline, key string) (string, bool) {
	for _, f := range strings.Fields(cmdline) {
		if name, value, ok := strings.Cut(f, "="); ok && name == key {
			return value, true
		}
	}
	return "", false
}

// identityTemplate is written next to grub.cfg: the values the image was built
// with, commented out, so that changing one is a matter of deleting a #.
//
// identityTemplate - grub.cfg 옆에 쓰는 파일 내용. 이미지를 빌드할 때 쓴 값을
// 주석 처리해 담아 두어, 하나를 바꾸려면 # 만 지우면 된다.
func identityTemplate(cmdline string) []byte {
	var b strings.Builder
	b.WriteString(`# vibeldr identity - read by GRUB before the kernel starts.
#
# Uncomment a line and change it to override what this image was built with.
# Nothing here is checked or rebuilt: whatever is set is what the kernel is
# told, so a MAC belongs here in the same 12 hex digit form as the built value.
#
# DSM treats the serial and the MAC as the machine's identity. Changing either
# after DSM is installed makes it a different machine as far as Synology's own
# services are concerned.
#
# syno_hw_version only moves between models built from the same .pat - the
# kernel and ramdisk in this image belong to one of them. For another platform,
# build an image for that model instead.

`)
	for _, kv := range identityVars(cmdline) {
		fmt.Fprintf(&b, "# set %s=%s\n", kv[0], kv[1])
	}
	return []byte(b.String())
}

// builderRamdisk is the loader's own environment, one file holding one program.
//
// builderRamdisk - 로더 자체 환경. 프로그램 하나를 담은 파일 하나다.
const builderRamdisk = "initrd-vibeldr"

// modulePack turns a driver directory into one archive for partition 4.
//
// It is a cpio rather than a directory on a filesystem because this partition
// has no filesystem. The helper reads it whole off the block device and unpacks
// it in memory. It is the format the kernel itself uses for a ramdisk, and the
// loader already knows how to read and write it.
//
// modulePack - 드라이버 디렉터리를 파티션 4 용 아카이브 하나로 바꾼다.
//
// 파일시스템 위의 디렉터리가 아니라 cpio 인 이유는 이 파티션에 파일시스템이
// 없기 때문이다. 헬퍼가 블록 장치에서 통째로 읽어와 메모리에서 푼다. 커널
// 자체가 ramdisk 로 쓰는 그 포맷이고, 로더가 이미 읽고 쓸 줄 안다.
func modulePack(dir string) ([]byte, int, error) {
	if dir == "" {
		return nil, 0, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}
	a := ramdisk.NewArchive()
	var n int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ko") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, 0, err
		}
		if err := a.Add(e.Name(), ramdisk.ModeRegular|0o644, data); err != nil {
			return nil, 0, err
		}
		n++
	}
	if n == 0 {
		return nil, 0, fmt.Errorf("%s has no .ko files in it", dir)
	}
	out, err := a.Bytes()
	if err != nil {
		return nil, 0, err
	}
	return out, n, nil
}

// packPartitionMB is how much room to give the pack: its size rounded up, plus
// room for Synology's originals, which the install adds from the .pat (14 MiB
// at most on the three models), and for a slightly larger rebuild.
//
// packPartitionMB - 팩에 줄 자리. 크기를 올림하고, 설치가 .pat 에서 더하는
// 시놀 원본(세 모델에서 최대 14 MiB)과 조금 커진 재빌드가 들어갈 여유를 더한다.
func packPartitionMB(size int) int {
	if size == 0 {
		return 0
	}
	const mib = 1 << 20
	return (size+mib-1)/mib + 32
}

// rebuildBootBinaryIfStale rebuilds the vibeldr-boot binary when it is older
// than its sources.
//
// The image builder only reads a prebuilt binary and puts it in. So fixing the
// source and reburning only the image puts the old binary in unchanged. The
// difference shows up only inside the image, which takes a long time to notice.
//
// Where there is no Go toolchain there is no way to rebuild, so it warns and
// carries on with the binary it has.
//
// rebuildBootBinaryIfStale - vibeldr-boot 바이너리가 소스보다 오래됐으면
// 다시 빌드한다.
//
// 이미지 빌더는 미리 만들어 둔 바이너리를 읽어 넣기만 한다. 그래서 소스를
// 고치고 이미지만 다시 구우면 옛 바이너리가 그대로 들어간다. 이미지 안에서만
// 드러나는 차이라서 알아채기까지 오래 걸린다.
//
// Go 툴체인이 없는 환경에서는 다시 빌드할 방법이 없으므로, 경고만 하고 있는
// 바이너리로 진행한다.
func rebuildBootBinaryIfStale(binPath string) error {
	newest, err := newestSourceTime("cmd/vibeldr-boot", "internal")
	if err != nil {
		// With no source tree there is nothing to check.
		// 소스 트리가 없으면 확인할 것도 없다.
		return nil
	}
	// The ramdisk helper is not a .go file but an embedded binary. If it was just
	// reburned and vibeldr-boot is not rebuilt, the old helper goes onto the image
	// unchanged.
	//
	// 램디스크 헬퍼는 .go 가 아니라 embed 된 바이너리다. 이게 새로 구워졌는데
	// vibeldr-boot 을 안 다시 빌드하면 옛 헬퍼가 그대로 이미지에 실린다.
	if st, err := os.Stat(initBinPath); err == nil && st.ModTime().After(newest) {
		newest = st.ModTime()
	}
	st, statErr := os.Stat(binPath)
	if statErr == nil && st.ModTime().After(newest) {
		return nil
	}

	goBin, lookErr := exec.LookPath("go")
	if lookErr != nil {
		if statErr == nil {
			ui.Warn("vibeldr-boot is older than its source but go was not found; using it as is (%s)", binPath)
			return nil
		}
		return fmt.Errorf("no vibeldr-boot binary and go was not found either: %w", lookErr)
	}

	ui.Info("rebuilding vibeldr-boot (the source is newer)")
	cmd := exec.Command(goBin, "build", "-o", binPath, "./cmd/vibeldr-boot")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vibeldr-boot build failed: %w\n%s", err, out)
	}
	return nil
}

// initBinPath is where the ramdisk helper is embedded. internal/initbin reads
// this path with //go:embed.
//
// initBinPath - 램디스크 헬퍼가 embed 되는 자리. internal/initbin 이 여기를
// //go:embed 로 읽는다.
const initBinPath = "internal/initbin/vibeldr-init.bin"

// rebuildInitBinaryIfStale reburns the ramdisk helper when it is older than its
// sources.
//
// Why it is needed:
//
//	cmd/vibeldr-init is a separate program and building this tool does not
//	build it. A prebuilt binary is simply embedded. So fixing the helper's
//	code and reburning only the image leaves the fix out entirely, and the
//	burn still succeeds and the machine still boots. All that shows on the
//	outside is "fixed it and it does not take", which sends the search off in
//	the wrong direction.
//
// Unlike vibeldr-boot, this does not stop at a warning. A missing helper leaves
// an image that looks perfectly fine and behaves differently, which is not
// something to pass over quietly.
//
// rebuildInitBinaryIfStale - 램디스크 헬퍼가 소스보다 오래됐으면 다시 굽는다.
//
// 왜 필요한가:
//
//	cmd/vibeldr-init 은 별개 프로그램이라 이 툴을 빌드해도 같이 빌드되지
//	않고, 미리 구워 둔 바이너리가 embed 될 뿐이다. 그래서 헬퍼 코드를 고치고
//	이미지만 다시 구우면 고친 내용이 통째로 빠지는데, 굽기는 성공하고 부팅도
//	된다. 겉으로는 "고쳤는데 안 먹는다" 로만 보여 엉뚱한 곳을 찾게 된다.
//
// vibeldr-boot 과 달리 경고로 끝내지 않는다. 헬퍼가 빠지면 이미지는 멀쩡해
// 보이는데 동작만 다르기 때문에, 조용히 넘어가면 안 된다.
func rebuildInitBinaryIfStale() error {
	newest, err := newestSourceTime("cmd/vibeldr-init", "internal")
	if err != nil {
		// With no source tree - only the distributed tool - there is nothing to check.
		// 소스 트리가 없으면 (배포된 툴만 있는 경우) 확인할 것도 없다.
		return nil
	}
	st, statErr := os.Stat(initBinPath)
	if statErr == nil && st.ModTime().After(newest) {
		return nil
	}

	goBin, lookErr := exec.LookPath("go")
	if lookErr != nil {
		if statErr == nil {
			ui.Warn("the ramdisk helper is older than its source but go was not found; using it as is (%s)", initBinPath)
			return nil
		}
		return fmt.Errorf("no ramdisk helper and go was not found either: %w", lookErr)
	}

	ui.Info("rebuilding the ramdisk helper (vibeldr-init) (the source is newer)")
	cmd := exec.Command(goBin, "build", "-o", initBinPath, "./cmd/vibeldr-init")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH=amd64", "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("vibeldr-init build failed: %w: %s", err, out)
	}
	return nil
}

// newestSourceTime is the most recent modification time among the .go files
// under the given directories.
//
// newestSourceTime - 주어진 디렉터리들 아래 .go 파일 중 가장 최근 수정 시각.
func newestSourceTime(dirs ...string) (time.Time, error) {
	var newest time.Time
	seen := false
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			seen = true
			if info.ModTime().After(newest) {
				newest = info.ModTime()
			}
			return nil
		})
		if err != nil {
			return newest, err
		}
	}
	if !seen {
		return newest, fmt.Errorf("no source files found")
	}
	return newest, nil
}

// renderBayPlan turns the settings' bay order into the file planted in the
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
