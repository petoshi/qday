package main

import (
	"os"
	"os/exec"

	"golang.org/x/sys/unix"
)

func lockFile(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func hideWindow(*exec.Cmd)      {}
func openBrowser(url string) error {
	cmd := exec.Command("open", url)
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait()
	return nil
}
func showError(message string) {
	_ = exec.Command("osascript",
		"-e", "on run argv",
		"-e", `display alert "QDAY Wallet" message (item 1 of argv) as critical`,
		"-e", "end run",
		"--", message).Run()
}
