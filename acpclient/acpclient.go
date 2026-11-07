// Package acpclient runs an ACP agent as a child process and routes its
// session traffic to one Handler per session. It knows nothing of Godwit or of
// any one agent. It offers the agent no files and no terminal.
package acpclient

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"sync"

	"github.com/coder/acp-go-sdk"
)

// Handler receives one session's traffic.
type Handler interface {
	Update(acp.SessionUpdate)
	Permission(acp.RequestPermissionRequest) acp.RequestPermissionOutcome
}

// Options start an agent: Command in Dir, with exactly Env. Stderr gets its stderr.
type Options struct {
	Command    []string
	Dir        string
	Env        []string
	Stderr     io.Writer
	ClientName string
}

// Client is a running agent.
type Client struct {
	Conn *acp.ClientSideConnection
	Init acp.InitializeResponse

	cmd      *exec.Cmd
	stdin    io.Closer
	mu       sync.Mutex
	handlers map[acp.SessionId]Handler
	once     sync.Once
}

// Start runs the agent and completes the handshake.
func Start(ctx context.Context, o Options) (*Client, error) {
	cmd := exec.Command(o.Command[0], o.Command[1:]...)
	cmd.Dir, cmd.Env, cmd.Stderr = o.Dir, o.Env, o.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &Client{cmd: cmd, stdin: stdin, handlers: map[acp.SessionId]Handler{}}
	c.Conn = acp.NewClientSideConnection(peer{c}, stdin, stdout)
	stop := context.AfterFunc(ctx, c.Close) // the handshake has no deadline of its own
	defer stop()
	c.Init, err = c.Conn.Initialize(ctx, acp.InitializeRequest{
		ProtocolVersion: acp.ProtocolVersionNumber,
		ClientInfo:      &acp.Implementation{Name: o.ClientName, Version: "1"}, // truthful: agents key behaviour off it
		ClientCapabilities: acp.ClientCapabilities{
			Fs:       acp.FileSystemCapabilities{ReadTextFile: false, WriteTextFile: false},
			Terminal: false,
		},
	})
	if err != nil {
		c.Close()
		return nil, err
	}
	return c, nil
}

// Done is closed once the agent's connection ended.
func (c *Client) Done() <-chan struct{} { return c.Conn.Done() }

// Handle routes session id to h, until the returned func runs.
func (c *Client) Handle(id acp.SessionId, h Handler) (remove func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[id] = h
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.handlers, id)
	}
}

// Close ends the agent.
func (c *Client) Close() {
	c.once.Do(func() {
		c.stdin.Close()
		if c.cmd.Process != nil {
			c.cmd.Process.Kill()
		}
		c.cmd.Wait()
	})
}

func (c *Client) handler(id acp.SessionId) Handler {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.handlers[id]
}

// peer is what the agent may call on us.
type peer struct{ c *Client }

var errRefused = errors.New("this client offers no files or terminal")

func (p peer) SessionUpdate(_ context.Context, n acp.SessionNotification) error {
	if h := p.c.handler(n.SessionId); h != nil {
		h.Update(n.Update)
	}
	return nil
}

func (p peer) RequestPermission(_ context.Context, r acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	h := p.c.handler(r.SessionId)
	if h == nil { // a session no one asked about: nothing is allowed
		return acp.RequestPermissionResponse{Outcome: acp.NewRequestPermissionOutcomeCancelled()}, nil
	}
	return acp.RequestPermissionResponse{Outcome: h.Permission(r)}, nil
}

func (peer) ReadTextFile(context.Context, acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	return acp.ReadTextFileResponse{}, errRefused
}
func (peer) WriteTextFile(context.Context, acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	return acp.WriteTextFileResponse{}, errRefused
}
func (peer) CreateTerminal(context.Context, acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	return acp.CreateTerminalResponse{}, errRefused
}
func (peer) KillTerminal(context.Context, acp.KillTerminalRequest) (acp.KillTerminalResponse, error) {
	return acp.KillTerminalResponse{}, errRefused
}
func (peer) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, errRefused
}
func (peer) ReleaseTerminal(context.Context, acp.ReleaseTerminalRequest) (acp.ReleaseTerminalResponse, error) {
	return acp.ReleaseTerminalResponse{}, errRefused
}
func (peer) WaitForTerminalExit(context.Context, acp.WaitForTerminalExitRequest) (acp.WaitForTerminalExitResponse, error) {
	return acp.WaitForTerminalExitResponse{}, errRefused
}
