package runner

import "sync"

// ArtifactCapabilityEnv names the exec env variable that carries the per-run
// artifact delivery capability (CP-003). The name avoids KEY, SECRET and TOKEN
// so env allowlists and the log redactor treat it like the other per-exec
// capabilities.
const ArtifactCapabilityEnv = "GOPOD_ARTIFACT_CAP"

// ArtifactIssuer issues per-exec artifact delivery capabilities. Declared at
// the consumer and satisfied in cmd/gopod. An empty token means none.
type ArtifactIssuer interface {
	IssueArtifact(folder string, thread int) (token string, end func())
}

// SetArtifactIssuer wires the issuer. Nil (the default, and always while
// GOPOD_OUTBOX_ARTIFACTS is off) means no exec gets a capability.
func (r *Runner) SetArtifactIssuer(i ArtifactIssuer) { r.artifacts = i }

// withArtifactCapability adds the artifact capability to an interactive exec's
// env and chains its revocation onto release, so one release after the streams
// close ends every per-exec capability. Anything missing (no issuer, a system
// run, a refused issue) leaves env and release untouched: fail closed.
func (r *Runner) withArtifactCapability(env []string, release func(), folder string, thread int, interactive bool) ([]string, func()) {
	if r.artifacts == nil || !interactive {
		return env, release
	}
	tok, end := r.artifacts.IssueArtifact(folder, thread)
	if tok == "" {
		if end != nil {
			end()
		}
		return env, release
	}
	var once sync.Once
	return append(env, ArtifactCapabilityEnv+"="+tok), func() {
		release()
		if end != nil {
			once.Do(end)
		}
	}
}
