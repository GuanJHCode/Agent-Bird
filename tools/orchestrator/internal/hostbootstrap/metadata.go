// Package hostbootstrap owns token-free disk metadata and the cooperative
// publication lease. It grants no Store, execution, or recovery authority.
package hostbootstrap

const MaxBytes = 64 * 1024

type Error string

func (e Error) Error() string { return string(e) }

type MetadataV2 struct {
	OwnerLaunchProtocol int           `json:"owner_launch_protocol,omitempty"`
	Version             int           `json:"version"`
	SocketPath          string        `json:"socket_path"`
	SpoolRoot           string        `json:"spool_root"`
	ProducerID          string        `json:"producer_id"`
	Hello               HelloMetadata `json:"hello"`
}
type HelloMetadata struct {
	LaunchID        string `json:"launch_id"`
	OriginContextID string `json:"origin_context_id"`
	OriginPID       int    `json:"origin_pid"`
	OriginBirth     string `json:"origin_birth"`
	HostGeneration  string `json:"host_generation"`
	PID             int    `json:"pid"`
	Birth           string `json:"birth"`
	Executable      string `json:"executable"`
}
