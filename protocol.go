package main

// The Godwit native plugin ABI (sdk/godwit_plugin.h): JSON in, an envelope out.

import (
	_ "embed"
	"encoding/json"
)

//go:embed plugin.json
var manifestJSON []byte

type pluginError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func failure(code, message string) *pluginError { return &pluginError{Code: code, Message: message} }

func envelope(result any, err *pluginError) []byte {
	var value any = struct {
		OK     bool `json:"ok"`
		Result any  `json:"result"`
	}{true, result}
	if err != nil {
		value = struct {
			OK    bool         `json:"ok"`
			Error *pluginError `json:"error"`
		}{false, err}
	}
	b, marshalErr := json.Marshal(value)
	if marshalErr != nil || len(b) > 4<<20 {
		return []byte(`{"ok":false,"error":{"code":"internal","message":"Response exceeds ABI limits.","retryable":false}}`)
	}
	return b
}

var thePlugin = &plugin{flows: map[string]*flow{}}

func dispatch(method string, data []byte) []byte {
	bad := envelope(nil, failure("invalid_request", "Invalid method or request JSON."))
	var req struct {
		DataDir string `json:"dataDir"`
		Flow    string `json:"flow"`
	}
	if len(data) > 0 && json.Unmarshal(data, &req) != nil {
		return bad
	}
	p := thePlugin
	switch method {
	case "describe":
		var m struct {
			Name, Version string
			Native        struct{ ABI int }
			Capabilities  []string
		}
		if json.Unmarshal(manifestJSON, &m) != nil {
			return envelope(nil, failure("internal", "Invalid embedded manifest."))
		}
		return envelope(map[string]any{"name": m.Name, "version": m.Version, "abi": m.Native.ABI, "capabilities": m.Capabilities,
			"methods": []string{"models.list", "login.status", "login.start", "login.poll", "login.cancel", "login.logout"}}, nil)
	case "configure":
		if req.DataDir == "" {
			return envelope(nil, failure("invalid_request", "No data directory."))
		}
		p.configure(req.DataDir)
		return envelope(struct{}{}, nil)
	case "models.list":
		return envelope(p.listModels())
	case "login.status":
		return envelope(p.loginStatus(), nil)
	case "login.start":
		return envelope(p.loginStart(), nil)
	case "login.poll":
		return envelope(p.loginPoll(req.Flow))
	case "login.cancel":
		p.loginCancel(req.Flow)
		return envelope(struct{}{}, nil)
	case "login.logout":
		return envelope(p.logout())
	}
	return bad
}
