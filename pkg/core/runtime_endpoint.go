package core

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"runtime"
	"strings"
)

// runtimeEndpoint returns the platform-native local endpoint for one Core
// service. Unix systems use a filesystem socket; Windows uses a named pipe so
// the endpoint does not depend on Unix socket support in the runtime.
func runtimeEndpoint(runDir, service string) string {
	return runtimeEndpointForOS(runDir, service, runtime.GOOS)
}

func runtimeEndpointForOS(runDir, service, goos string) string {
	if goos != "windows" {
		return filepath.Join(runDir, service+".sock")
	}
	identity := strings.ReplaceAll(filepath.Clean(runDir), `\`, "/")
	hash := sha256.Sum256([]byte(identity))
	name := hex.EncodeToString(hash[:])[:12]
	return `pipe:\\.\pipe\qrypt-` + name + "-" + service
}
