package main

import (
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

func lockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func hideWindow(*exec.Cmd)      {}
func openBrowser(url string) error {
	cmd := exec.Command("xdg-open", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
func showError(message string) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return
	}
	if program, err := exec.LookPath("zenity"); err == nil {
		_ = exec.Command(program, "--error", "--title=QDAY Wallet", "--text="+message).Run()
	}
}
