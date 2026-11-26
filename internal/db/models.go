package db

import "time"

const (
	KeychainPassword = "password"
	KeychainSSHKey   = "ssh_key"

	TunnelLocal   = "local"
	TunnelRemote  = "remote"
	TunnelDynamic = "dynamic"

	LogStatusSuccess = "Success"
	LogStatusFailed  = "Failed"
)

// Counts holds live row counts for sidebar badge rendering.
type Counts struct {
	Hosts        int
	Keychain     int
	PortForwards int
	Snippets     int
	Logs         int
}

// KeychainItem represents an encrypted password identity or SSH key reference.
type KeychainItem struct {
	ID             int64
	Name           string
	Type           string // "password" | "ssh_key"
	Username       string
	SecretEnc      []byte
	PrivateKeyPath string
	PublicKey      string
	CreatedAt      time.Time
}

// Host represents a saved SSH connection target.
type Host struct {
	ID              int64
	Name            string
	Hostname        string
	Port            int
	Username        string
	OSType          string
	GroupName       string
	Tags            string
	KeychainID      *int64
	KeychainName    string // populated on read via LEFT JOIN
	KeychainType    string // populated on read via LEFT JOIN
	InlineSecretEnc []byte
	RecordSession   bool
	Notes           string
	LastConnectedAt *time.Time
	CreatedAt       time.Time
}

// PortForward represents a Local (-L), Remote (-R), or Dynamic (-D) SSH tunnel.
type PortForward struct {
	ID         int64
	Name       string
	HostID     int64
	HostName   string // populated on read via JOIN
	HostTarget string // populated on read via JOIN (user@hostname:port)
	Type       string // "local" | "remote" | "dynamic"
	LocalAddr  string
	LocalPort  int
	RemoteAddr string
	RemotePort int
	CreatedAt  time.Time
}

// Snippet represents a reusable multi-line command snippet.
type Snippet struct {
	ID        int64
	Name      string
	Category  string
	Command   string
	CreatedAt time.Time
}

// ConnectionLog represents a historical SSH session or snippet audit log.
type ConnectionLog struct {
	ID             int64
	HostID         *int64
	HostName       string
	TargetAddr     string
	Status         string
	DurationSec    int
	TranscriptPath string
	StartedAt      time.Time
}
