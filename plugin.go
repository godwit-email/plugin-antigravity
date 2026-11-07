package main

// The plugin's state: one long-lived ACP server, its Antigravity sign-in, and
// the loopback server Godwit's SDK talks to.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/acp-go-sdk"
	"github.com/godwit-email/plugin-antigravity/acpclient"
)

const (
	loginMethod = "oauth-personal"
	authPrompt  = "Open the following link to authenticate the ACP server: " // the server prints the page here, never on stdout
	// Each session keeps a harness process of about 135 MB, and the server cannot
	// end one (session/close answers and frees nothing, as of 1.4.5): restarting it is
	// the only way. This bounds it near 0.9 GB, for a few seconds' start every so often.
	recycleAfter = 5
)

// agentCommand is how the ACP server starts; tests swap it for a fake.
var agentCommand = func(dataDir string) []string { return []string{filepath.Join(runtimeDir(dataDir), serverName)} }

type plugin struct {
	mu       sync.Mutex
	dataDir  string
	agent    *acpclient.Client
	sessions int // sessions on agent so far
	turns    int // turns running on agent
	models   []modelInfo
	base     string
	token    string
	tools    sync.Map // MCP id to the []requestTool of a running session
	flows    map[string]*flow
}

type modelInfo struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Transport   string         `json:"transport"`
	Protocol    string         `json:"protocol"`
	BaseURL     string         `json:"baseURL"`
	Description map[string]any `json:"description,omitempty"`
}

func (p *plugin) configure(dataDir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.dataDir = dataDir
}

func (p *plugin) home() string { return filepath.Join(p.dataDir, "home") }

// agentOptions isolate the server in home: its settings, its sessions. The
// environment Godwit gives a plugin has no HOME, which the macOS keychain needs.
func (p *plugin) agentOptions(stderr io.Writer) (acpclient.Options, error) {
	dir := filepath.Join(p.home(), "antigravity-acp")
	cwd := filepath.Join(p.dataDir, "cwd") // empty: there is nothing for the agent here, nor any tool to read it
	for _, d := range []string{dir, cwd} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return acpclient.Options{}, err
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"auth":{"type":"`+loginMethod+`"}}`), 0o600); err != nil {
		return acpclient.Options{}, err
	}
	u, err := user.Current()
	if err != nil {
		return acpclient.Options{}, err
	}
	env := append(os.Environ(), "HOME="+u.HomeDir, "GEMINI_HOME="+p.home(),
		"ANTIGRAVITY_HARNESS_PATH="+filepath.Join(runtimeDir(p.dataDir), harnessName),
		"BROWSER=/usr/bin/true") // Godwit opens the page, once
	return acpclient.Options{Command: agentCommand(p.dataDir), Dir: cwd, Env: env, Stderr: stderr, ClientName: "godwit"}, nil
}

// wipeSessions removes what the server keeps of past sessions: the prompts,
// and with them mail. Call it with no session running.
func (p *plugin) wipeSessions() {
	for _, d := range []string{"brain", "conversations"} {
		os.RemoveAll(filepath.Join(p.home(), "antigravity-acp", d))
	}
}

// forgetSession removes what the server keeps of one finished session.
func (p *plugin) forgetSession(id acp.SessionId) {
	if id == "" { // no session was made; an empty name would match all of them
		return
	}
	dir := filepath.Join(p.home(), "antigravity-acp")
	os.RemoveAll(filepath.Join(dir, "brain", string(id)))
	files, _ := filepath.Glob(filepath.Join(dir, "conversations", string(id)+"*"))
	for _, f := range files {
		os.Remove(f)
	}
}

// startTurn gives a running turn the agent; endTurn must follow.
func (p *plugin) startTurn(ctx context.Context) (*acpclient.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for p.agent != nil && p.sessions >= recycleAfter && p.turns > 0 { // running turns need their sessions
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			p.mu.Lock()
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
		p.mu.Lock()
	}
	if p.agent != nil {
		select {
		case <-p.agent.Done():
			p.agent.Close()
			p.agent = nil
		default:
		}
	}
	if p.agent != nil && p.sessions >= recycleAfter {
		p.agent.Close()
		p.agent = nil
	}
	if p.agent == nil {
		if p.turns == 0 {
			p.wipeSessions() // a crash may have left some
		}
		o, err := p.agentOptions(os.Stderr)
		if err != nil {
			return nil, err
		}
		if p.agent, err = acpclient.Start(context.WithoutCancel(ctx), o); err != nil {
			return nil, err
		}
		p.sessions = 0
	}
	p.sessions++
	p.turns++
	return p.agent, nil
}

func (p *plugin) endTurn(id acp.SessionId) {
	p.forgetSession(id)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.turns--
}

// builtinsOff is the session option that turns every tool of the agent off
// (ADR 0033). If a server stops honoring it, turn.Update ends the turn.
func builtinsOff(extra map[string]any) map[string]any {
	m := map[string]any{"enabledTools": []string{}}
	for k, v := range extra {
		m[k] = v
	}
	return map[string]any{"antigravity": m}
}

// configOption is a session's select option, as the server sends it.
type configOption struct {
	ID           string `json:"id"`
	Category     string `json:"category"`
	CurrentValue string `json:"currentValue"`
	Options      []struct {
		Value string `json:"value"`
		Name  string `json:"name"`
	} `json:"options"`
}

func modelOption(opts []acp.SessionConfigOption) (configOption, bool) {
	b, _ := json.Marshal(opts)
	var parsed []configOption
	json.Unmarshal(b, &parsed)
	for _, o := range parsed {
		if o.ID == "model" || o.Category == "model" {
			return o, true
		}
	}
	return configOption{}, false
}

func isAuthError(err error) bool {
	var re *acp.RequestError
	return errors.As(err, &re) && (re.Code == -32000 || strings.Contains(strings.ToLower(re.Message), "authentication"))
}

// listModels asks the server through a session of its own: that is the only
// way it says, and the way to learn whether the user is signed in.
func (p *plugin) listModels() (any, *pluginError) {
	p.mu.Lock()
	if p.models != nil {
		defer p.mu.Unlock()
		return map[string]any{"models": p.models}, nil
	}
	installed := p.dataDir != "" && runtimeInstalled(p.dataDir)
	p.mu.Unlock()
	if !installed {
		return nil, failure("login_required", "Sign in to Google Antigravity first.")
	}
	base, err := p.serve()
	if err != nil {
		return nil, failure("internal", err.Error())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	agent, err := p.startTurn(ctx)
	if err != nil {
		return nil, failure("upstream", err.Error())
	}
	s, err := agent.Conn.NewSession(ctx, acp.NewSessionRequest{Cwd: filepath.Join(p.dataDir, "cwd"), McpServers: []acp.McpServer{}, Meta: builtinsOff(nil)})
	if err != nil {
		p.endTurn("")
		if isAuthError(err) {
			return nil, failure("login_required", "Sign in to Google Antigravity first.")
		}
		return nil, failure("upstream", err.Error())
	}
	defer p.endTurn(s.SessionId)
	opt, ok := modelOption(s.ConfigOptions)
	if !ok {
		return nil, failure("upstream", "Antigravity lists no models.")
	}
	models := make([]modelInfo, 0, len(opt.Options))
	for _, o := range opt.Options {
		models = append(models, modelInfo{ID: o.Value, Name: o.Name, Transport: "http", Protocol: "openai-completions", BaseURL: base})
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models = models
	return map[string]any{"models": models}, nil
}

// notice is what Godwit shows before the user signs in.
var notice = map[string]string{
	"default": "This may breach Google Antigravity's terms, which forbid reaching the service through third-party software: Google can suspend your Antigravity and Gemini CLI access. Mail you send to these models is stored by Google and may be read by its reviewers. The plugin downloads Google's server, about 100 MB. On this Mac it shares its sign-in with other editors that use Antigravity through ACP, and signing out here signs them out too. A Gemini API key under Models is the supported way.",
	"zh-Hans": "这可能违反 Google Antigravity 条款，条款禁止通过第三方软件访问服务，Google 可能暂停你的 Antigravity 和 Gemini CLI 使用权。你发给这些模型的邮件会被 Google 存储，审核人员可能查看。插件要下载 Google 的服务程序，约 100 MB。这台 Mac 上的其他 ACP 编辑器和它共用同一个 Antigravity 登录，在这里退出也会退出它们。受支持的做法是在模型设置里使用 Gemini API key。",
}

func (p *plugin) loginStatus() any {
	_, perr := p.listModels()
	if perr != nil {
		return map[string]any{"signedIn": false, "notice": notice}
	}
	return map[string]any{"signedIn": true, "account": "Google", "notice": notice}
}

func (p *plugin) logout() (any, *pluginError) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.models = nil
	if !runtimeInstalled(p.dataDir) {
		return struct{}{}, nil
	}
	if p.agent == nil {
		o, err := p.agentOptions(os.Stderr)
		if err != nil {
			return nil, failure("internal", err.Error())
		}
		if p.agent, err = acpclient.Start(context.Background(), o); err != nil {
			return nil, failure("upstream", err.Error())
		}
		p.sessions = 0
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := p.agent.Conn.Logout(ctx, acp.LogoutRequest{}); err != nil {
		return nil, failure("upstream", err.Error())
	}
	if p.turns == 0 { // it may still hold the token in memory
		p.agent.Close()
		p.agent = nil
	}
	return struct{}{}, nil
}

// flow is a sign-in: a server of its own, so that giving up can simply end it.
type flow struct {
	mu      sync.Mutex
	state   string
	url     string
	message string
	cancel  context.CancelFunc
}

func (f *flow) set(state, url, message string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != "done" && f.state != "failed" {
		f.state, f.url, f.message = state, url, message
	}
}

func (p *plugin) loginStart() any {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	f := &flow{state: "preparing", cancel: cancel}
	id := rand.Text()
	p.mu.Lock()
	p.flows[id] = f
	p.mu.Unlock()
	go func() {
		defer cancel()
		if err := p.signIn(ctx, f); err != nil {
			f.set("failed", "", err.Error())
			return
		}
		p.mu.Lock()
		p.models = nil // the list and the running server were for no account
		if p.agent != nil && p.turns == 0 {
			p.agent.Close()
			p.agent = nil
		}
		p.mu.Unlock()
		f.set("done", "", "")
	}()
	return map[string]string{"flow": id}
}

func (p *plugin) signIn(ctx context.Context, f *flow) error {
	p.mu.Lock()
	dataDir := p.dataDir
	p.mu.Unlock()
	if err := installRuntime(ctx, dataDir); err != nil {
		return err
	}
	pr, pw := io.Pipe()
	defer pw.Close()
	go watchAuthPrompt(pr, f)
	o, err := p.agentOptions(io.MultiWriter(pw, os.Stderr))
	if err != nil {
		return err
	}
	c, err := acpclient.Start(ctx, o)
	if err != nil {
		return err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, c.Close) // the server waits for the browser, for as long as it likes
	defer stop()
	_, err = c.Conn.Authenticate(ctx, acp.AuthenticateRequest{MethodId: loginMethod})
	return err
}

// watchAuthPrompt finds the sign-in page in the server's stderr. Only Google's
// page will do: this is a line of text a child process printed.
func watchAuthPrompt(r io.Reader, f *flow) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if _, after, ok := strings.Cut(sc.Text(), authPrompt); ok {
			if u, err := url.Parse(strings.TrimSpace(after)); err == nil && u.Scheme == "https" && u.Host == "accounts.google.com" {
				f.set("open", u.String(), "")
			}
		}
	}
	io.Copy(io.Discard, r)
}

func (p *plugin) loginPoll(id string) (any, *pluginError) {
	p.mu.Lock()
	f := p.flows[id]
	p.mu.Unlock()
	if f == nil {
		return nil, failure("invalid_request", "No such sign-in.")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]string{"state": f.state, "url": f.url, "account": "Google", "message": f.message}, nil
}

func (p *plugin) loginCancel(id string) {
	p.mu.Lock()
	f := p.flows[id]
	delete(p.flows, id)
	p.mu.Unlock()
	if f != nil {
		f.cancel()
	}
}

// serve starts the loopback server once and returns the base URL of its API.
// The random path is the only key: other programs on this computer must not
// spend the user's quota.
func (p *plugin) serve() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.base != "" {
		return p.base, nil
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	b := make([]byte, 16)
	rand.Read(b)
	p.token = hex.EncodeToString(b)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /"+p.token+"/v1/chat/completions", p.chatCompletions)
	mux.HandleFunc("/"+p.token+"/mcp/{id}", p.mcp)
	go http.Serve(l, mux)
	p.base = fmt.Sprintf("http://%s/%s/v1", l.Addr(), p.token)
	return p.base, nil
}

// compactJSON is v as one line of JSON text, never failing on what JSON decoded.
func compactJSON(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if enc.Encode(v) != nil {
		return "{}"
	}
	return strings.TrimSpace(buf.String())
}
