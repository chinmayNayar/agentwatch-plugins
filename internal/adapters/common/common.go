// Package common holds the helpers every adapter shares: tool classification
// and tolerant field access on decoded JSON.
package common

import (
	"encoding/json"
	"github.com/chinmayNayar/agentwatch-plugins/internal/lazyre"
	"strings"

	"github.com/chinmayNayar/agentwatch-plugins/internal/event"
)

// Str returns m[k] as a string ("" when absent or not a scalar).
func Str(m map[string]any, keys ...string) string {
	for _, k := range keys {
		switch v := m[k].(type) {
		case string:
			if v != "" {
				return v
			}
		case float64:
			b, _ := json.Marshal(v)
			return string(b)
		case bool:
			if v {
				return "true"
			}
			return "false"
		}
	}
	return ""
}

// Map returns m[k] as an object. A JSON string holding an object is decoded
// (Copilot's toolArgs and Cursor's tool_output can arrive as strings).
func Map(m map[string]any, k string) map[string]any {
	switch v := m[k].(type) {
	case map[string]any:
		return v
	case string:
		var out map[string]any
		if json.Unmarshal([]byte(v), &out) == nil {
			return out
		}
		if v != "" {
			return map[string]any{"value": v}
		}
	}
	return nil
}

// Int returns a pointer to m[k] as an int, or nil.
func Int(m map[string]any, keys ...string) *int {
	for _, k := range keys {
		if f, ok := m[k].(float64); ok {
			n := int(f)
			return &n
		}
	}
	return nil
}

// Bool returns m[k] as a bool.
func Bool(m map[string]any, k string) bool {
	b, _ := m[k].(bool)
	return b
}

// Text renders any decoded JSON value as text for size/exit-code purposes.
func Text(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

var (
	readTools = set("read", "read_file", "readfile", "view", "grep", "glob", "ls", "list", "list_dir",
		"listdir", "search", "codebase_search", "file_search", "find", "notebookread", "bashoutput",
		"todoread", "semantic_search", "get_errors", "list_code_usages")
	writeTools = set("write", "edit", "multiedit", "notebookedit", "delete", "apply_patch", "create",
		"create_file", "replace_string_in_file", "insert_edit_into_file", "str_replace_editor",
		"str_replace_based_edit_tool", "edit_file", "write_file", "delete_file", "multi_replace_string_in_file",
		"edit_notebook_file")
	shellTools = set("bash", "shell", "exec_command", "run_in_terminal", "runinterminal", "terminal",
		"powershell", "killshell", "kill_shell", "run_command", "local_shell")
	netTools  = set("webfetch", "web_fetch", "fetch", "fetch_webpage", "websearch", "web_search", "browser")
	metaTools = set("todowrite", "task", "agent", "exitplanmode", "update_plan", "skill", "slashcommand",
		"subagenthandback", "think", "askuserquestion")
)

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

var netCmd = lazyre.New(`(?i)(^|[\s;&|(])(curl|wget|nc|ncat|scp|rsync|ssh|ftp|sftp|telnet|http|httpie|Invoke-WebRequest|iwr|Invoke-RestMethod)(\s|$)`)

// Classify maps a tool to a class and pulls out the fields the server checks.
// MCP tools: Claude Code/Codex `mcp__server__tool`, Cursor `MCP:tool`.
func Classify(tool string, input map[string]any) (class, command, path, mcpServer, mcpTool string) {
	t := strings.TrimSpace(tool)
	lt := strings.ToLower(t)
	command = Str(input, "command", "cmd", "script")
	path = Str(input, "file_path", "path", "filePath", "notebook_path", "target_file", "filename", "file")
	switch {
	case strings.HasPrefix(lt, "mcp__"):
		parts := strings.SplitN(t[5:], "__", 2)
		mcpServer = parts[0]
		if len(parts) == 2 {
			mcpTool = parts[1]
		}
		return event.ClassMCP, command, path, mcpServer, mcpTool
	case strings.HasPrefix(lt, "mcp:"):
		mcpTool = strings.TrimSpace(t[4:])
		return event.ClassMCP, command, path, Str(input, "server", "mcp_server_name"), mcpTool
	case readTools[lt]:
		return event.ClassRead, command, path, "", ""
	case writeTools[lt]:
		if path == "" && lt == "apply_patch" {
			path = patchPath(command)
		}
		return event.ClassWrite, command, path, "", ""
	case shellTools[lt]:
		if netCmd.MatchString(command) {
			return event.ClassNetwork, command, path, "", ""
		}
		return event.ClassShell, command, path, "", ""
	case netTools[lt]:
		if u := Str(input, "url"); u != "" && command == "" {
			command = u
		}
		return event.ClassNetwork, command, path, "", ""
	case metaTools[lt]:
		return event.ClassMeta, command, path, "", ""
	}
	return event.ClassShell, command, path, "", ""
}

var patchFile = lazyre.New(`(?m)^\*\*\* (?:Update|Add|Delete) File: (.+)$`)

func patchPath(patch string) string {
	if m := patchFile.FindStringSubmatch(patch); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// PatchPaths lists every file an apply_patch touches.
func PatchPaths(patch string) []string {
	var out []string
	for _, m := range patchFile.FindAllStringSubmatch(patch, -1) {
		out = append(out, strings.TrimSpace(m[1]))
	}
	return out
}

// Marshal is json.Marshal without HTML escaping (clients read raw JSON).
func Marshal(v any) []byte {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return []byte(strings.TrimRight(b.String(), "\n"))
}
