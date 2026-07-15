//go:build !windows

package main

import (
	"fmt"
	"syscall"
)

func hiddenProcAttr() *syscall.SysProcAttr { return nil }
func setDPIAware()                         {}
func runAppWindow(string) error            { return fmt.Errorf("WebView2 chỉ hỗ trợ Windows") }
func fatalDialog(msg string)               { fmt.Println(msg) }
