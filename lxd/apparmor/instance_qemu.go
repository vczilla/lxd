package apparmor

import (
	"text/template"
)

var qemuProfileTpl = template.Must(template.New("qemuProfile").Parse(`#include <tunables/global>
profile "{{ .name }}" flags=(attach_disconnected,mediate_deleted) {
  #include <abstractions/base>
  #include <abstractions/consoles>
  #include <abstractions/nameservice>

  capability dac_override,
  capability dac_read_search,
  capability ipc_lock,
  capability setgid,
  capability setuid,
  capability sys_chroot,
  capability sys_resource,

  # Needed by qemu
  /dev/hugepages/**                         rw,
  /dev/kvm                                  rw,
  /dev/net/tun                              rw,
  /dev/ptmx                                 rw,
  /dev/vfio/**                              rw,
  /dev/vhost-net                            rw,
  /dev/vhost-vsock                          rw,
  /etc/ceph/**                              r,
  /run/udev/data/*                          r,
  /sys/bus/                                 r,
  /sys/bus/nd/devices/                      r,
  /sys/bus/usb/devices/                     r,
  /sys/bus/usb/devices/**                   r,
  /sys/class/                               r,
  /sys/devices/**                           r,
  /sys/module/vhost/**                      r,
  /{,usr/}bin/qemu*                         mrix,
  {{ .ovmfPath }}/OVMF_CODE.fd              kr,
  /data/local/usr/share/qemu/**                        kr,
  /data/local/usr/share/seabios/**                     kr,
  owner @{PROC}/@{pid}/task/@{tid}/comm     rw,
  {{ .rootPath }}/etc/nsswitch.conf         r,
  {{ .rootPath }}/etc/passwd                r,
  {{ .rootPath }}/etc/group                 r,
  @{PROC}/version                           r,

  # Instance specific paths
  {{ .logPath }}/** rwk,
  {{ .path }}/** rwk,
{{range $index, $element := .devPaths}}
  {{$element}} rwk,
{{- end }}

  # Needed for lxd fork commands
  {{ .exePath }} mr,
  @{PROC}/@{pid}/cmdline r,
  {{ .rootPath }}/{etc,lib,usr/lib}/os-release r,

  # Things that we definitely don't need
  deny @{PROC}/@{pid}/cgroup r,
  deny /sys/module/apparmor/parameters/enabled r,
  deny /sys/kernel/mm/transparent_hugepage/hpage_pmd_size r,

{{- if .snap }}
  # The binary itself (for nesting)
  /var/snap/lxd/common/lxd.debug            mr,
  /snap/lxd/*/bin/lxd                       mr,
  /snap/lxd/*/bin/qemu*                     mrix,
  /snap/lxd/*/share/qemu/**                 kr,

  # Snap-specific paths
  /var/snap/lxd/common/ceph/**              r,
  {{ .rootPath }}/etc/ceph/**               r,

  # Snap-specific libraries
  /snap/lxd/*/lib/**.so*            mr,
{{- end }}

{{if .libraryPath -}}
  # Entries from LD_LIBRARY_PATH
{{range $index, $element := .libraryPath}}
  {{$element}}/** mr,
{{- end }}
{{- end }}

{{- if .raw }}

  ### Configuration: raw.apparmor
{{ .raw }}
{{- end }}
}
`))
