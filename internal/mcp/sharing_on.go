//go:build !noshare

package mcp

// sharingCompiled is false in a `-tags noshare` build, where the share
// network surface (stream.go) is not compiled in and sharing is always off.
const sharingCompiled = true
