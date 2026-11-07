# Antigravity plugin for Godwit

Use Gemini through a Google Antigravity sign-in, as a native Godwit plugin. The design and its risks are in Godwit's [ADR 0033](https://github.com/godwit-email/godwit/blob/main/docs/adr/0033-antigravity-native-plugin-and-plugin-login.md).

**Read this first.** Antigravity's [terms](https://antigravity.google/terms) forbid reaching the service through third-party software, and Google can suspend your Antigravity and Gemini CLI access for it. Google stores what you send, and its reviewers may read it: here, that is your mail. A Gemini API key under Godwit's model settings is the supported way. This plugin is not in Godwit's plugin directory, on purpose.

macOS only: elsewhere Google's server keeps its token in a plain file.

## How it works

- Godwit's SDK talks OpenAI chat to a loopback server in the plugin. Each request becomes one session on Google's ACP server, with the whole history.
- The agent's own tools are off (`enabledTools: []`). If it tries one anyway, the turn stops and the request fails.
- A tool the model calls is not run here: the agent's permission request becomes `tool_calls`, the turn is cancelled, and Godwit handles the call with its usual approvals.
- The server's copies of each session are deleted after the turn, since they hold mail.
- The token stays in the macOS keychain, where Google's server keeps it. The plugin never reads it.

## Build

```bash
python3 scripts/package.py
```

Then in Godwit: Settings → Extensions → install from a folder, and pick `dist/antigravity`.

## Before changing the server version

Run the probe from ADR 0033 again against the new version. If `enabledTools: []` no longer turns the built-in tools off, do not ship it.

## License

The plugin is Apache-2.0. Google's server is proprietary, and is downloaded from Google at sign-in, not redistributed.
