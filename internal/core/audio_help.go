package core

import "runtime"

// Documentation links shown in the UI and in actionable error messages.
const (
	FFmpegDownloadURL      = "https://ffmpeg.org/download.html"
	FFmpegWindowsBuildsURL = "https://www.gyan.dev/ffmpeg/builds/"
	WhisperCPPBuildURL     = "https://github.com/ggml-org/whisper.cpp#building"
	WhisperCPPReleasesURL  = "https://github.com/ggml-org/whisper.cpp/releases"
	WhisperCPPHomebrewURL  = "https://formulae.brew.sh/formula/whisper.cpp"
	VulkanDriverURL        = "https://vulkan.lunarg.com/sdk/home"
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
		if WhisperRuntimeInstalledFor(WhisperBackendAuto) {
			return "Local Whisper uses the Apple GPU (Metal) automatically; no setup is needed."
		}
		return `Install whisper-cli first: run "brew install whisper.cpp", then restart noesis. It then uses the Apple GPU (Metal) automatically.`
	}
	if !WhisperRuntimeInstallSupportedFor(WhisperBackendAuto) {
		return "Automatic runtime download is not available for this platform. Install whisper-cli manually. Guide: " + WhisperCPPBuildURL
	}
	return "No setup needed: noesis downloads the best whisper.cpp runtime automatically (Vulkan GPU when available, otherwise CPU) and switches back to the CPU if the GPU is unusable."
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
		return `Install whisper-cli with "brew install whisper.cpp", then restart noesis; it then uses the Apple GPU (Metal) automatically. Guide: ` + WhisperCPPBuildURL
	}
	if !WhisperRuntimeInstallSupportedFor(WhisperBackendAuto) {
		return `Install whisper-cli manually. Guide: ` + WhisperCPPBuildURL
	}
	return `Noesis downloads the whisper.cpp runtime automatically when you download a model or transcribe audio. If that failed, check your connection or install whisper-cli manually. Guide: ` + WhisperCPPBuildURL
}

// WhisperVulkanInstallInstructions returns a user-facing note about the Vulkan
// runtime. Vulkan is normally chosen and downloaded automatically; this text is
// only shown when an explicit Vulkan request cannot be satisfied.
func WhisperVulkanInstallInstructions() string {
	if runtime.GOOS == "darwin" {
		return "macOS uses the Apple GPU (Metal) instead of Vulkan; no setup is needed."
	}
	if !WhisperVulkanInstallSupported() {
		return `Vulkan acceleration is not published for this platform, so noesis uses the CPU automatically. Guide: ` + WhisperCPPBuildURL
	}
	return `Noesis uses Vulkan automatically when your graphics driver supports it and falls back to the CPU otherwise; no setup is needed. If the automatic download failed, retry, or set NOESIS_WHISPER_VULKAN_URL to a Vulkan whisper.cpp archive. Guide: ` + WhisperCPPBuildURL
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
