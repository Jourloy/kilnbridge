package platform

import "os/exec"

// LaunchSettings is shared by the download launcher and the worker's tray.
func LaunchSettings(exe, dir string) error {
	cmd := exec.Command(exe, "--data", dir)
	// HideWindow would override WebView2's first ShowWindow call. The release
	// executable uses the GUI subsystem and already opens without a console.
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
