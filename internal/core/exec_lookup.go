package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// findExecutable looks in PATH and in common install locations that GUI apps
// often miss (for example Homebrew's bin directory on macOS).
func findExecutable(name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	var dirs []string
	switch runtime.GOOS {
	case "darwin":
		dirs = []string{"/opt/homebrew/bin", "/usr/local/bin", "/opt/local/bin"}
	case "linux":
		dirs = []string{"/usr/local/bin", "/usr/bin", "/snap/bin"}
	case "windows":
		if local := os.Getenv("LOCALAPPDATA"); local != "" {
			dirs = append(dirs, filepath.Join(local, "Microsoft", "WinGet", "Links"))
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		dirs = append(dirs, filepath.Join(home, ".local", "bin"))
	}
	names := []string{name}
	if runtime.GOOS == "windows" && filepath.Ext(name) == "" {
		names = append(names, name+".exe")
	}
	for _, dir := range dirs {
		for _, candidate := range names {
			path := filepath.Join(dir, candidate)
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			if runtime.GOOS != "windows" && info.Mode()&0o111 == 0 {
				continue
			}
			return path
		}
	}
	return ""
}
