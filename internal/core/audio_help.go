package core

import "runtime"

// Documentation links shown in the UI and in actionable error messages.
const (
	FFmpegDownloadURL      = "https://ffmpeg.org/download.html"
	FFmpegWindowsBuildsURL = "https://www.gyan.dev/ffmpeg/builds/"
	WhisperCPPBuildURL     = "https://github.com/ggml-org/whisper.cpp#building"
	WhisperCPPReleasesURL  = "https://github.com/ggml-org/whisper.cpp/releases"
	WhisperCPPHomebrewURL  = "https://formulae.brew.sh/formula/whisper.cpp"
	GoogleSpeechDocsURL    = "https://cloud.google.com/speech-to-text/docs"
	GoogleSpeechEnableURL  = "https://console.cloud.google.com/apis/library/speech.googleapis.com"
	GoogleCredentialsURL   = "https://console.cloud.google.com/apis/credentials"
)

// FFmpegAvailable reports whether ffmpeg can be found locally.
func FFmpegAvailable() bool {
	return findExecutable("ffmpeg") != ""
}

// LocalWhisperSetupNote returns a short platform-specific note for the UI.
func LocalWhisperSetupNote() string {
	if runtime.GOOS == "darwin" {
		return `On macOS install whisper-cli first: run "brew install whisper.cpp", then restart noesis.`
	}
	if !WhisperRuntimeAutoInstallSupported() {
		return `Automatic runtime download is not available for this platform. Install whisper-cli manually. Guide: ` + WhisperCPPBuildURL
	}
	return "The whisper.cpp runtime downloads automatically with the first model."
}

// FFmpegInstallExample returns a one-line install command for the current OS.
func FFmpegInstallExample() string {
	switch runtime.GOOS {
	case "darwin":
		return `macOS: brew install ffmpeg, then restart noesis`
	case "windows":
		return `Windows: winget install Gyan.FFmpeg, then restart noesis`
	default:
		return `Linux: sudo apt install ffmpeg (Debian/Ubuntu) or sudo dnf install ffmpeg (Fedora), then restart noesis`
	}
}

// FFmpegInstallInstructions returns an actionable error message for a missing
// ffmpeg binary.
func FFmpegInstallInstructions() string {
	switch runtime.GOOS {
	case "darwin":
		return `Install it with "brew install ffmpeg", then restart noesis. More: ` + FFmpegDownloadURL
	case "windows":
		return `Install it with "winget install Gyan.FFmpeg" (or download a build from ` + FFmpegWindowsBuildsURL + `), then restart noesis.`
	default:
		return `Install it with your package manager (for example "sudo apt install ffmpeg" or "sudo dnf install ffmpeg"), then restart noesis. More: ` + FFmpegDownloadURL
	}
}

// WhisperRuntimeInstallInstructions returns an actionable error message for a
// missing whisper.cpp runtime.
func WhisperRuntimeInstallInstructions() string {
	if runtime.GOOS == "darwin" {
		return `Install whisper-cli with "brew install whisper.cpp", then restart noesis. Guide: ` + WhisperCPPBuildURL
	}
	if !WhisperRuntimeAutoInstallSupported() {
		return `Install whisper-cli manually. Guide: ` + WhisperCPPBuildURL
	}
	return `Open Settings → Whisper models and download it automatically, or install whisper-cli manually. Guide: ` + WhisperCPPBuildURL
}

// GoogleAPIKeyInstructions returns an actionable error message for a missing
// Google Speech-to-Text API key.
func GoogleAPIKeyInstructions() string {
	return `Enable Speech-to-Text at ` + GoogleSpeechEnableURL + `, then create an API key at ` + GoogleCredentialsURL + ` and paste it in Settings. You can also set GOOGLE_API_KEY.`
}

// GoogleSpeechEnableInstructions returns an actionable error message for Google
// API permission or billing failures.
func GoogleSpeechEnableInstructions() string {
	return `Check that Speech-to-Text is enabled and billing is active: ` + GoogleSpeechEnableURL
}
