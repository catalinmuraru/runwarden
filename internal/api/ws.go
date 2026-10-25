package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"

	"github.com/catalinmuraru/runwarden/internal/audit"
	"github.com/catalinmuraru/runwarden/internal/executor"
)

const (
	wsChunk        = 16 << 10
	wsReadLimit    = 64 << 10
	wsPingInterval = 30 * time.Second
)

// Client → server messages.
type wsIn struct {
	Type   string `json:"type"` // input, close_input, stop, kill
	Line   string `json:"line,omitempty"`
	Secret bool   `json:"secret,omitempty"` // hidden input, masked in the output
}

// Server → client messages.
type wsOutput struct {
	Type   string `json:"type"` // "output"
	Data   string `json:"data"`
	Offset int    `json:"offset"` // offset after this chunk; reconnect with ?offset= to resume
}

type wsResult struct {
	Type            string     `json:"type"` // "result"
	Status          string     `json:"status"`
	ExitCode        *int       `json:"exit_code,omitempty"`
	Finished        *time.Time `json:"finished,omitempty"`
	OutputTruncated bool       `json:"output_truncated,omitempty"`
}

type wsError struct {
	Type    string `json:"type"` // "error"
	Message string `json:"message"`
}

// runSocket streams a run's output from ?offset= on, then its result. While the run is live,
// the client can send input and stop or kill it.
func (s *Server) runSocket(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := s.runMeta(r, id); !ok {
		writeError(w, http.StatusNotFound, "unknown run")
		return
	}
	run := s.exec.Get(id)
	// Accept rejects cross-origin requests unless the host is in AllowedOrigins.
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.opts.AllowedOrigins})
	if err != nil {
		return
	}
	defer func() { _ = c.CloseNow() }()
	c.SetReadLimit(wsReadLimit)

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	offset = max(offset, 0)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	if run != nil {
		go s.socketCommands(ctx, cancel, c, run, r)
		err = s.streamLive(ctx, c, r, run, offset)
	} else {
		ctx = c.CloseRead(ctx)
		err = s.streamStored(ctx, c, r, id, offset)
	}
	if err == nil {
		_ = c.Close(websocket.StatusNormalClosure, "")
	}
}

func (s *Server) socketCommands(ctx context.Context, cancel context.CancelFunc, c *websocket.Conn, run *executor.Run, r *http.Request) {
	defer cancel()
	for {
		var msg wsIn
		if err := wsjson.Read(ctx, c, &msg); err != nil {
			return
		}
		var err error
		switch msg.Type {
		case "input":
			err = run.WriteInput(msg.Line, msg.Secret)
			s.recordRunAction(r, audit.RunInput, run, err)
		case "close_input":
			err = run.CloseInput()
		case "stop":
			run.Stop()
			s.recordRunAction(r, audit.RunStop, run, nil)
		case "kill":
			run.Kill()
			s.recordRunAction(r, audit.RunKill, run, nil)
		default:
			err = errors.New("unknown message type")
		}
		if err != nil {
			_ = wsjson.Write(ctx, c, wsError{Type: "error", Message: err.Error()})
		}
	}
}

func (s *Server) streamLive(ctx context.Context, c *websocket.Conn, r *http.Request, run *executor.Run, offset int) error {
	ping := time.NewTicker(wsPingInterval)
	defer ping.Stop()
	for {
		changed := run.Changed()
		data, next, done := run.Output(offset)
		if !done {
			// Hold back a trailing partial UTF-8 character until the rest arrives.
			cut := completeUTF8(data)
			data, next = data[:cut], offset+cut
		}
		if err := sendOutput(ctx, c, data, offset); err != nil {
			return err
		}
		offset = next
		if done {
			return s.sendResult(ctx, c, r, run.ID)
		}
		select {
		case <-changed:
		case <-ping.C:
			if err := c.Ping(ctx); err != nil {
				return err
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func (s *Server) streamStored(ctx context.Context, c *websocket.Conn, r *http.Request, id string, offset int) error {
	f, err := s.hist.OpenOutput(id)
	if err != nil {
		return s.sendResult(ctx, c, r, id)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(int64(offset), io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return err
	}
	if err := sendOutput(ctx, c, data, offset); err != nil {
		return err
	}
	return s.sendResult(ctx, c, r, id)
}

// sendOutput sends data in chunks that end on whole UTF-8 characters.
func sendOutput(ctx context.Context, c *websocket.Conn, data []byte, offset int) error {
	for len(data) > 0 {
		n := min(len(data), wsChunk)
		if n < len(data) {
			if cut := completeUTF8(data[:n]); cut > 0 {
				n = cut
			}
		}
		offset += n
		if err := wsjson.Write(ctx, c, wsOutput{Type: "output", Data: string(data[:n]), Offset: offset}); err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func (s *Server) sendResult(ctx context.Context, c *websocket.Conn, r *http.Request, id string) error {
	meta, ok := s.runMeta(r, id)
	if !ok {
		return wsjson.Write(ctx, c, wsError{Type: "error", Message: "unknown run"})
	}
	return wsjson.Write(ctx, c, wsResult{
		Type:            "result",
		Status:          meta.Status,
		ExitCode:        meta.ExitCode,
		Finished:        meta.Finished,
		OutputTruncated: meta.OutputTruncated,
	})
}

// completeUTF8 returns the length of the longest prefix of b that does not end inside a
// multi-byte UTF-8 character.
func completeUTF8(b []byte) int {
	for i := len(b) - 1; i >= 0 && i >= len(b)-utf8.UTFMax; i-- {
		if utf8.RuneStart(b[i]) {
			if utf8.FullRune(b[i:]) {
				return len(b)
			}
			return i
		}
	}
	return len(b)
}
