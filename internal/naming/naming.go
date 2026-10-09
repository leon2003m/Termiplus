// Package naming holds the naming scheme shared by the gateway and the
// socket proxy. The proxy enforces these patterns, the gateway produces them.
package naming

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
)

const (
	// ManagedLabel marks every container and volume this system owns.
	ManagedLabel = "termiplus.managed"
	ManagedValue = "true"

	LabelUser    = "termiplus.user"
	LabelPolicy  = "termiplus.policy"
	LabelConfig  = "termiplus.config"
	LabelStorage = "termiplus.storage" // persistent | ephemeral

	ContainerPrefix = "ot-u-"
	EphemeralPrefix = "ot-e-"
	VolumePrefix    = "ot-home-"

	// HomeDir is the only mount target allowed inside a terminal container.
	HomeDir = "/home/user"
)

var (
	// suffix = 16 hex chars of sha256(user id) + "-" + policy name
	suffixPattern = `[0-9a-f]{16}-[a-z0-9]{1,24}`

	ContainerName = regexp.MustCompile(`^` + ContainerPrefix + `(` + suffixPattern + `)$`)
	// ephemeral: 16 hex user + "-" + 12 hex session (chat) + "-" + policy
	EphemeralName = regexp.MustCompile(`^` + EphemeralPrefix + `([0-9a-f]{16})-[0-9a-f]{12}-[a-z0-9]{1,24}$`)
	VolumeName    = regexp.MustCompile(`^` + VolumePrefix + `(` + suffixPattern + `)$`)
	PolicyName    = regexp.MustCompile(`^[a-z0-9]{1,24}$`)
	// Open WebUI user ids are UUIDs; allow a conservative superset.
	UserID = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,128}$`)
	// Label values the proxy accepts for the gateway's own labels.
	LabelValue = regexp.MustCompile(`^[A-Za-z0-9._@:-]{1,128}$`)
)

// Suffix returns the per (user, policy) identifier used in names.
func Suffix(userID, policy string) string {
	sum := sha256.Sum256([]byte(userID))
	return hex.EncodeToString(sum[:8]) + "-" + policy
}

func Container(userID, policy string) string { return ContainerPrefix + Suffix(userID, policy) }
func Volume(userID, policy string) string    { return VolumePrefix + Suffix(userID, policy) }

// Ephemeral returns the name of a throwaway container for one session (chat).
func Ephemeral(userID, sessionID, policy string) string {
	u := sha256.Sum256([]byte(userID))
	c := sha256.Sum256([]byte(sessionID))
	return EphemeralPrefix + hex.EncodeToString(u[:8]) + "-" + hex.EncodeToString(c[:6]) + "-" + policy
}

// IsManagedContainer reports whether name is a terminal container name.
func IsManagedContainer(name string) bool {
	return ContainerName.MatchString(name) || EphemeralName.MatchString(name)
}

// UserHash returns the 16 hex user part of a managed container or volume name.
func UserHash(name string) string {
	for _, re := range []*regexp.Regexp{ContainerName, VolumeName, EphemeralName} {
		if m := re.FindStringSubmatch(name); m != nil {
			return m[1][:16]
		}
	}
	return ""
}
