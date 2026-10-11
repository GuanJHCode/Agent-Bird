package gitfilter

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GuanJHCode/Agent-Bird/tools/orchestrator/internal/process"
)

var ErrCarrierHelper = errors.New("filter_carrier_helper_rejected")

// All routing fields are comparison data, never a transform capability.
// Helper pin MUST originate from the future authenticated original Host owner;
// observing/hashing the current binary cannot grant caller authorization.
type CarrierHelperExecutableExpectation struct{ Path, SHA256 string }
type CarrierHelperNamedIdentity struct {
	Device, Inode, Nlink uint64
	UID, Mode            uint32
}
type CarrierHelperRoute struct {
	Domain                      string
	Host                        process.HostPeerExpectation
	Socket                      string
	Parent, Directory, SocketID CarrierHelperNamedIdentity
	Helper                      CarrierHelperExecutableExpectation
	Deadline                    time.Time
}

const carrierHelperDomain = "agent-bird-filter-carrier-routing-v1"
const carrierHelperRouteMax = 8192

func EncodeCarrierHelperRoute(r CarrierHelperRoute) (string, error) {
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > carrierHelperRouteMax || r.Domain != carrierHelperDomain || r.Deadline.IsZero() {
		return "", ErrCarrierHelper
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
func DecodeCarrierHelperRoute(encoded string) (CarrierHelperRoute, error) {
	var r CarrierHelperRoute
	if len(encoded) > 2*carrierHelperRouteMax {
		return r, ErrCarrierHelper
	}
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) > carrierHelperRouteMax {
		return r, ErrCarrierHelper
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&r) != nil || d.Decode(new(any)) != io.EOF || r.Domain != carrierHelperDomain || r.Deadline.IsZero() {
		return CarrierHelperRoute{}, ErrCarrierHelper
	}
	return r, nil
}

// RunCarrierHelper owns cancellable duplicates of Git's original pipe streams.
// Authentication precedes reading even the Git protocol greeting.
func RunCarrierHelper(ctx context.Context, r CarrierHelperRoute, input, output *os.File) error {
	if ctx == nil || r.Domain != carrierHelperDomain || input == nil || output == nil || !r.Deadline.After(time.Now()) {
		return ErrCarrierHelper
	}
	bounded, cancel := context.WithDeadline(ctx, r.Deadline)
	defer cancel()
	return runCarrierHelper(bounded, r, input, output)
}

// The domain-separated binding is made by the original owner, never from
// helper argv. The Gate must be created with this digest before construction.
func carrierTransportCommandDigest(base string, pin CarrierHelperExecutableExpectation) string {
	if !exactDigest(base) || !exactDigest(pin.SHA256) || !filepath.IsAbs(pin.Path) || filepath.Clean(pin.Path) != pin.Path || len(pin.Path) > 4096 || strings.IndexByte(pin.Path, 0) >= 0 {
		return ""
	}
	raw, _ := json.Marshal(struct {
		Command string
		Helper  CarrierHelperExecutableExpectation
	}{base, pin})
	sum := sha256.Sum256(append([]byte("agent-bird-filter-carrier-command-v1\x00"), raw...))
	return hex.EncodeToString(sum[:])
}
