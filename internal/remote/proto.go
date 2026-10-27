// Package remote connects executors to the server. An executor (one Deployment per
// environment, any number of pods) dials out to the server over one WebSocket, registers with
// its environment's token, then takes runs and dropdown helper calls over that connection.
// Output, exit status and helper output come back on the same connection. The server never
// dials an executor, and secret values never leave the executor.
package remote

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"time"

	"github.com/catalinmuraru/runwarden/internal/executor"
	"github.com/catalinmuraru/runwarden/internal/kube"
	"github.com/catalinmuraru/runwarden/internal/sandbox"
	"github.com/catalinmuraru/runwarden/internal/secrets"
)

// Path is where executors connect on the server.
const Path = "/api/executors/connect"

// EnvironmentHeader names the environment an executor registers for; the bearer token in
// Authorization must be that environment's executor token.
const EnvironmentHeader = "X-Runwarden-Environment"

// Protocol is the version of the message set below. Server and executor must agree.
const Protocol = 1

// Message types. "→ executor" messages are sent by the server, "→ server" by the executor.
const (
	typeHello    = "hello"    // → server: registration (Hello)
	typeWelcome  = "welcome"  // → executor: registration accepted
	typeStart    = "start"    // → executor: start a run (Start)
	typeStarted  = "started"  // → server: the run is running (Started)
	typeFailed   = "failed"   // → server: a start or helper call failed (Error)
	typeInput    = "input"    // → executor: one line of input (Data, Secret)
	typeEOF      = "eof"      // → executor: end of input
	typeStop     = "stop"     // → executor: SIGTERM, then SIGKILL after the grace period
	typeKill     = "kill"     // → executor: SIGKILL
	typeResize   = "resize"   // → executor: terminal size (Rows, Cols)
	typeOutput   = "output"   // → server: masked output (Data)
	typeExit     = "exit"     // → server: the run has ended (Exit)
	typeCapture  = "capture"  // → executor: run a dropdown helper (Capture)
	typeCaptured = "captured" // → server: helper output (Data)
)

// errBusy is the Error of a start refused because the executor runs as many runs as allowed.
const errBusy = "busy"

// Message is one WebSocket text message, JSON encoded. ID names the run or the helper call.
type Message struct {
	Type    string   `json:"type"`
	ID      string   `json:"id,omitempty"`
	Hello   *Hello   `json:"hello,omitempty"`
	Start   *Start   `json:"start,omitempty"`
	Started *Started `json:"started,omitempty"`
	Capture *Capture `json:"capture,omitempty"`
	Exit    *Exit    `json:"exit,omitempty"`
	Data    []byte   `json:"data,omitempty"`
	Secret  bool     `json:"secret,omitempty"`
	Rows    int      `json:"rows,omitempty"`
	Cols    int      `json:"cols,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// Hello registers an executor pod: what it is and what it holds, never secret values.
type Hello struct {
	Protocol  int            `json:"protocol"`
	Version   string         `json:"version"`
	Pod       string         `json:"pod"`
	Capacity  int            `json:"capacity"` // concurrent runs it accepts
	Isolation sandbox.Status `json:"isolation"`
	Secrets   []secrets.Info `json:"secrets"`
	Cluster   *kube.Status   `json:"cluster,omitempty"` // its environment's kubeconfig, if any
}

// Start asks the executor to run Job. The executor adds its environment's secrets and
// kubeconfig itself; Files are the run's uploaded files, which it writes to its own disk and
// whose server paths it replaces in the job.
type Start struct {
	Job        executor.Job `json:"job"`
	Secrets    []string     `json:"secrets,omitempty"` // names the script declares
	Kubernetes bool         `json:"kubernetes,omitempty"`
	Files      []File       `json:"files,omitempty"`
}

// File is one uploaded file of a run.
type File struct {
	Path string `json:"path"` // where the server stored it: the value the job refers to
	Data []byte `json:"data"`
}

// Started confirms a start, with what may be shown about the cluster the run was pinned to.
type Started struct {
	Clusters []kube.Cluster `json:"clusters,omitempty"`
}

// Exit is a run's final result.
type Exit struct {
	Status    executor.Status `json:"status"`
	ExitCode  int             `json:"exit_code"`
	Finished  time.Time       `json:"finished"`
	Truncated bool            `json:"truncated,omitempty"`
}

// Capture asks the executor to run a dropdown helper and return its output.
type Capture struct {
	Command    []string `json:"command"`
	Dir        string   `json:"dir"`
	Secrets    []string `json:"secrets,omitempty"`
	Kubernetes bool     `json:"kubernetes,omitempty"`
}

// HashToken returns the hex SHA-256 of an executor token, as the server is configured with.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// validID accepts the run IDs the server creates; the executor uses them as directory names.
var validID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
