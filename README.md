# vibeldr

*[한국어 README](README.ko.md)*

[![latest release](https://img.shields.io/github/v/release/jnhkchoi/vibeldr)](https://github.com/jnhkchoi/vibeldr/releases/latest)

A from-scratch x86 bootloader that runs **Synology DSM 7.4.1-90080** on ordinary
PC hardware, with a **graphical (framebuffer) install wizard** — pick a model,
lay out disks and NICs with the mouse, and it patches the DSM kernel and writes
the boot disk for you.

**[Download the loader image](https://github.com/jnhkchoi/vibeldr/releases/latest)**
— `vibeldr-loader.img.gz` (79 MB). Write it to a USB stick and boot; see
[Install](#install).

> **Disclaimer.** vibeldr is an independent research project. It is **not**
> affiliated with, authorized by, or endorsed by Synology. DSM itself is
> Synology's copyrighted software and is **not** included here — you supply your
> own `.pat`. Use it on hardware you own, for learning and personal use.

> **About this project.** This began purely out of curiosity about how
> Xpenology-style loaders work, and was built largely through AI-assisted
> ("vibe") coding. The author does not maintain the codebase and generally
> **cannot fix bugs or add features** — it is shared **as-is**, as a curiosity.

## Supported models

| Model | Platform | Kernel | Bays |
|---|---|---|---|
| DS918+ | apollolake | 4.4.302 | 4 |
| DS3622xs+ | broadwellnk | 4.4.302 | 12 |
| SA6400 | epyc7002 | 5.10.55 | 12 |

All on DSM **7.4.1-90080**. Verified end-to-end (install → DSM web setup) under
KVM/QEMU (Proxmox), booting from a USB loader disk.

## What makes it different

- **Graphical install wizard**, not a text menu. It renders on `/dev/fb0` with a
  sidebar, dropdowns, and a mouse cursor, and walks through 8 steps
  (start → model → boot disk → storage → network → serial → confirm → install).
- **No kernel module (LKM).** Instead of intercepting calls at runtime, vibeldr:
  - **patches the DSM `bzImage`** before boot to accept unsigned modules
    (boot-param lock `LOCK OR → AND`, ramdisk-check `JZ → JMP`), computing the
    byte offsets from the kernel image each time — no hardcoded addresses;
  - injects a small **userspace helper** into the DSM ramdisk that maps disk
    bays, forces the NIC MAC, neutralizes firmware flashers, and fixes the drive
    compatibility DB.
- **Forces the real NIC hardware MAC**, so your router/DHCP sees the chosen MAC.
- **LAN order and power button**: the wizard lets you choose which card is
  LAN 1, LAN 2 and so on, and the power button (or Proxmox's Shutdown) shuts DSM
  down cleanly.
- **Auto storage mapping**: `SataPortMap`/`DiskIdxMap` derived from the detected
  controllers for legacy platforms, device-tree slots for DT platforms.
- **DSM kernel fetched at install.** It comes from Synology's own download
  servers and is unpacked on the machine, not shipped with the loader.
- **Fits DSM to a PC after the install**: M.2 NVMe SSDs usable as cache on
  DS918+ and SA6400, drivers for USB network cards plugged in later, the loader
  stick kept out of DSM's external devices, and a new loader offering the serial
  number and MACs an installed DSM already has.
- **Diagnostics bundle**: one button saves the loader's and DSM's logs to the
  stick, with the serial number, MACs and notification settings left out.

## Requirements

- A target x86-64 machine (or VM) you own.
- A CPU with the instructions the model's DSM kernel uses: MOVBE for DS918+,
  MOVBE, BMI1 and BMI2 for DS3622xs+ and SA6400. Intel Core from Haswell (4th
  generation) and AMD Zen have all three. The wizard checks this on the model
  step.
- A **USB** disk (~1 GB) for the loader — DSM only recognizes USB / SATA-DOM as
  its boot device.
- At least one data disk for DSM.
- Screen + keyboard + mouse for the wizard; a wired NIC for DSM.
- 2 GB of RAM or more: the install runs in memory.
- Internet access on the target during install — the loader fetches the DSM
  image straight from Synology, and the DSM web assistant then installs DSM
  itself (online, or from a `.pat` you upload). Without it, see
  [Without internet](#without-internet).

## Build

Optional — the released image is built with exactly this command, so you can
reproduce it yourself instead of trusting the binary. Go (the Linux helper is
built automatically; the tool itself runs on Windows/Linux/macOS):

```bash
go run ./cmd/vibeldr bootstrap
```

This produces `work/out/bootstrap.img`, a model-agnostic loader image. The DSM
`.pat` for the chosen model and its driver and firmware packs (from the latest
release of [vibeldr-modules](https://github.com/jnhkchoi/vibeldr-modules)) are
fetched at install time, so the target needs internet access during the install step.
The pack holds our builds only; Synology's own modules are taken from the `.pat`
and merged in during the install. `--driver-pack <cpio>` puts a pack on the
image beforehand.

## Install

The image boots the graphical wizard from `/dev/fb0`, so the target needs a
console/display. Each machine (or VM) needs its **own copy** of the image — the
loader writes the patched kernel back onto it.

### Proxmox / KVM

Run on the Proxmox host. The VM uses SeaBIOS + q35 and the default CPU type and
display; the loader is attached as a **removable USB** disk because DSM only
accepts USB / SATA-DOM as its boot device. The install runs in RAM: all three
models were tested with 4 GB, DS918+ also with 2 GB.

```bash
# 1) unpack a copy of the image into the ISO storage (one copy per VM)
gunzip -c vibeldr-loader.img.gz > /var/lib/vz/template/iso/vibeldr-900.img

# 2) create the VM (virtio, e1000e and rtl8139 NICs are tested)
qm create 900 --name vibeldr --machine q35 --cores 2 --memory 4096 \
  --net0 virtio=BC:24:11:00:00:01,bridge=vmbr0

# 3) a data disk for DSM (32 GB or more), on SATA: SA6400 does not take SCSI disks
qm set 900 --sata0 local-lvm:32

# 4) attach the loader image as a removable USB disk; bootindex=1 boots it first
qm set 900 --args "-device qemu-xhci,id=xhci,addr=0x18 \
  -drive file=/var/lib/vz/template/iso/vibeldr-900.img,if=none,id=drive-usb0,format=raw \
  -device usb-storage,bus=xhci.0,drive=drive-usb0,id=usb0,bootindex=1,removable=on"
qm set 900 --boot 'order=sata0;net0'

# 5) start it and open the console
qm start 900
```

A self-built image is `work/out/bootstrap.img`; copy it in place of step 1.

### Bare metal

1. Write the image to a USB stick (this **erases** the stick):
   - Windows: [balenaEtcher](https://etcher.balena.io/) takes the downloaded
     `.img.gz` as-is; Rufus needs it unzipped to `.img` first.
   - Linux/macOS: `gunzip -c vibeldr-loader.img.gz | sudo dd of=/dev/sdX bs=4M status=progress conv=fsync`
     — replace `/dev/sdX` with the USB device; **double-check the device name.**
2. In the target's BIOS/UEFI: the image boots in **BIOS mode**, so enable
   Legacy/CSM, disable **Secure Boot**, and set it to boot from the USB stick.
3. Power on — the wizard appears on screen. Follow it, then finish DSM setup in a
   browser via `http://find.synology.com` or `http://<the box's IP>:5000`.

### Without internet

Put these at the top of another USB stick or disk (FAT32, exFAT or ext4) and
plug it in before starting the wizard; they are used instead of downloading:

- the DSM `.pat` under its published name, e.g. `DSM_DS918+_90080.pat`
  (checked against the catalog's MD5)
- the driver pack from the
  [vibeldr-modules release](https://github.com/jnhkchoi/vibeldr-modules/releases),
  e.g. `apollolake-DS918+.cpio.gz`, with its `-firmware.cpio.gz` and `SHA256SUMS`

### Back to the loader from DSM

To change settings on a machine with no screen or keyboard, run this in DSM as
root (over SSH), then restart DSM. The next boot opens the loader once; the boot
after that is DSM again.

```sh
sudo /usr/sbin/vibeldr-agent -next-boot-loader
```

### Boot log without a serial port

Add `vibeldr_netconsole` to the kernel command line (in `loader.yaml` under
`cmdline:`, or by editing the DSM entry at the boot menu) and the kernel log is
broadcast to UDP port 6666 from the first network card. Read it from another
machine on the same network:

```sh
socat -u udp-recv:6666 -
```

The packets come from a link-local address (169.254.x.x), which a Linux
receiver drops unless it has a route there: add one first
(`sudo ip route add 169.254.0.0/16 dev <interface>`), or read with
`sudo tcpdump -l -A -i <interface> udp port 6666`. The whole kernel log is
sent, from the first line of the boot, with the loader's own `vibeldr:` lines
in order among it.

A value is passed to the kernel's netconsole as it is, e.g.
`vibeldr_netconsole=6665@192.168.0.114/eth0,6666@192.168.0.254/`.

### Locked out of DSM

Boot the loader's **text menu** entry and choose `D) Repair DSM on next boot`.
It can clear DSM's IP auto-block list (the allow list stays) and turn off
triggered tasks (boot-up / shutdown scripts). The repairs run when DSM next
starts; each database is copied to `<name>.vibeldr-bak` first, and the result
is written to `/var/log/vibeldr-tasks.log` inside DSM.

## Wizard walkthrough (SA6400 example)

The 8 steps are the same for every model; only the model card and the bay count
differ (DS918+ shows 4 bays, the others 12).

| | |
|---|---|
| **1. Start** — review detected disks/NICs | ![start](docs/img/wizard-1.png) |
| **2. Model** — pick the Synology model | ![model](docs/img/wizard-2.png) |
| **3. Boot disk** — the loader disk (not used as DSM storage) | ![boot disk](docs/img/wizard-3.png) |
| **4. Storage** — place disks into bays | ![storage](docs/img/wizard-4.png) |
| **5. Network** — LAN order and MAC (real MAC kept by default) | ![network](docs/img/wizard-5.png) |
| **6. Serial** — model-rule serial (auto or manual) | ![serial](docs/img/wizard-6.png) |
| **7. Confirm** — review the summary | ![confirm](docs/img/wizard-7.png) |
| **8. Install** — download DSM and the driver/firmware packs, add Synology's own modules, patch, write, reboot | ![install](docs/img/wizard-8.png) |

After it finishes, reboot into DSM and open the web UI to complete setup:

![DSM web](docs/img/dsm-web.png)

## How it works (short)

1. GRUB loads a small Alpine kernel + our initrd; the wizard runs on the
   framebuffer.
2. You pick a model and lay out disks/NICs. The loader downloads that model's
   DSM image from Synology, unpacks it, patches the `bzImage`, injects the
   userspace helper into the DSM ramdisk, writes the patched kernel to the boot
   partition, and stages a driver pack.
3. On reboot the DSM installer comes up and installs DSM online (or from a
   `.pat` you upload); setup is finished in the DSM web UI.

## Credits

vibeldr is original code, but it stands on the shoulders of the community loaders
and their research. In particular:

- **[RedPill](https://github.com/RedPill-TTG/redpill-lkm)** — the reference for
  what DSM checks at boot/install (sig-enforce, ramdisk check, firmware-flash
  blocking) that vibeldr reproduces without a kernel module.
- **[Arc](https://github.com/AuxXxilium)** (`AuxXxilium/arc-modules`) and
  **[M-shell / TinyCore-RedPill](https://github.com/PeterSuh-Q3)**
  (`PeterSuh-Q3/tcrp-modules`) — the modules vibeldr-modules takes from their
  packs where we lack a driver or theirs is better (its README lists each one),
  and a reference for `SataPortMap`/`DiskIdxMap` and flasher handling.
- **[ARPL / rr](https://github.com/RROrg/rr)** — general prior art for automated
  Synology loaders.
- **[007revad](https://github.com/007revad)**
  ([Synology_HDD_db](https://github.com/007revad/Synology_HDD_db),
  [Synology_M2_volume](https://github.com/007revad/Synology_M2_volume)) — the
  reference for making third-party drives pass DSM's compatibility DB / rule
  engine, which vibeldr applies automatically.

Thanks to everyone in the XPEnology community.

## License

To be decided by the author. Until a `LICENSE` file is added, all rights are
reserved. (These loaders commonly use GPL-3.0.)
