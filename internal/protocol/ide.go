package protocol

type IDEInstallSource string

const (
	IDEInstallConfigured IDEInstallSource = "configured"
	IDEInstallPath       IDEInstallSource = "path"
	IDEInstallManaged    IDEInstallSource = "managed"
	IDEInstallDownloaded IDEInstallSource = "downloaded"
)
