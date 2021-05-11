//go:build linux && cgo
// +build linux,cgo

package quota

// #cgo CFLAGS: -std=gnu11 -Wvla  -fvisibility=hidden -Winit-self
// #cgo CFLAGS: -Wformat=2 -Wshadow -Wendif-labels -fasynchronous-unwind-tables
// #cgo CFLAGS: -pipe --param=ssp-buffer-size=4 -g -Wunused
// #cgo CFLAGS: -Werror=implicit-function-declaration
// #cgo CFLAGS: -Werror=return-type -Wendif-labels -Werror=overflow
// #cgo CFLAGS: -Wnested-externs -fexceptions
// #cgo CFLAGS: -D__ANDROID_API__=29
// #cgo LDFLAGS: -L/system/lib64 -lc
import "C"
