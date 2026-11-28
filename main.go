package main

/*
#include <stdint.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxy_plugin_call(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxy_plugin_free(void*, size_t);
extern void cliproxy_plugin_shutdown(void);
*/
import "C"

import (
	"fmt"
	"unsafe"

	"github.com/monet88/antigravity-cloak/internal/clients/claudecode"
	"github.com/monet88/antigravity-cloak/internal/clients/codex"
	"github.com/monet88/antigravity-cloak/internal/clients/omp"
	"github.com/monet88/antigravity-cloak/internal/cloak"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const abiVersion = 1

const (
	pluginName       = "antigravity-cloak"
	pluginVersion    = "0.7.8"
	pluginRepository = "https://github.com/monet88/antigravity-cloak"
)

func init() {
	cloak.Initialize(
		claudecode.Spec(),
		codex.Spec(),
		omp.Spec(),
	)
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
	plugin.abi_version = abiVersion
	plugin.call = (C.cliproxy_plugin_call_fn)(C.cliproxy_plugin_call)
	plugin.free_buffer = (C.cliproxy_plugin_free_fn)(C.cliproxy_plugin_free)
	plugin.shutdown = (C.cliproxy_plugin_shutdown_fn)(C.cliproxy_plugin_shutdown)
	return 0
}

//export cliproxy_plugin_call
func cliproxy_plugin_call(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeCResponse(response, cloak.MustErrorEnvelope("invalid_method", "method is required"))
		return 1
	}

	methodName := C.GoString(method)
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}

	raw, code := handlePluginCall(methodName, requestBytes)
	writeCResponse(response, raw)
	return C.int(code)
}

//export cliproxy_plugin_free
func cliproxy_plugin_free(ptr unsafe.Pointer, _ C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
}

//export cliproxy_plugin_shutdown
func cliproxy_plugin_shutdown() {}

func writeCResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.malloc(C.size_t(len(raw)))
	if ptr == nil {
		return
	}
	C.memcpy(ptr, unsafe.Pointer(&raw[0]), C.size_t(len(raw)))
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}

func handlePluginCall(method string, request []byte) ([]byte, int) {
	switch method {
	case pluginabi.MethodPluginRegister, pluginabi.MethodPluginReconfigure:
		return cloak.HandlePluginLifecycle(request, registrationResponse()), 0
	case pluginabi.MethodRequestInterceptBefore:
		return cloak.HandleRequestInterceptBefore(request), 0
	case pluginabi.MethodRequestInterceptAfter:
		return cloak.MustEnvelope(pluginapi.RequestInterceptResponse{}), 0
	case pluginabi.MethodRequestComplete:
		return cloak.HandleRequestComplete(request), 0
	case pluginabi.MethodResponseInterceptAfter:
		return cloak.HandleResponseIntercept(request), 0
	case pluginabi.MethodResponseInterceptStreamChunk:
		return cloak.HandleStreamChunkIntercept(request), 0
	default:
		return cloak.MustErrorEnvelope("unknown_method", fmt.Sprintf("unknown method %q", method)), 0
	}
}

func registrationResponse() any {
	return struct {
		SchemaVersion uint32             `json:"schema_version"`
		Metadata      pluginapi.Metadata `json:"metadata"`
		Capabilities  struct {
			ModelRouter            bool `json:"model_router"`
			Executor               bool `json:"executor"`
			RequestInterceptor     bool `json:"request_interceptor"`
			RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
			ResponseInterceptor    bool `json:"response_interceptor"`
			StreamChunkInterceptor bool `json:"response_stream_interceptor"`
		} `json:"capabilities"`
	}{
		SchemaVersion: pluginabi.SchemaVersion,
		Metadata: pluginapi.Metadata{
			Name:             pluginName,
			Version:          pluginVersion,
			Author:           "local",
			GitHubRepository: pluginRepository,
			Logo:             "",
			ConfigFields:     configFields(),
		},
		Capabilities: struct {
			ModelRouter            bool `json:"model_router"`
			Executor               bool `json:"executor"`
			RequestInterceptor     bool `json:"request_interceptor"`
			RequestLifecyclePlugin bool `json:"request_lifecycle_plugin"`
			ResponseInterceptor    bool `json:"response_interceptor"`
			StreamChunkInterceptor bool `json:"response_stream_interceptor"`
		}{
			RequestInterceptor:     true,
			RequestLifecyclePlugin: true,
			ResponseInterceptor:    true,
			StreamChunkInterceptor: true,
		},
	}
}

func configFields() []pluginapi.ConfigField {
	return []pluginapi.ConfigField{
		{
			Name:        "use_default_keywords",
			Type:        pluginapi.ConfigFieldTypeBoolean,
			Description: "Enable the built-in coding software and agent keyword preset.",
		},
		{
			Name:        "custom_mappings",
			Type:        pluginapi.ConfigFieldTypeObject,
			Description: "Additional case-insensitive system-field rewrite mappings, for example Cursor: Antigravity.",
		},
		{
			Name:        "tool_mappings",
			Type:        pluginapi.ConfigFieldTypeObject,
			Description: "Custom tool name mappings per client. Keys: client name (claude_code, codex, oh_my_pi). Values: map of original_tool_name → antigravity_target_name. Overrides defaults for matching keys.",
		},
		{
			Name:        "model_prefixes",
			Type:        pluginapi.ConfigFieldTypeArray,
			Description: "Only cloak when the upstream/requested model starts with one of these prefixes, for example agy/. Leave empty to cloak for every model (matches all providers).",
		},
		{
			Name:        "omp_prompt_sanitizations",
			Type:        pluginapi.ConfigFieldTypeObject,
			Description: "Oh My Pi inbound sanitization mappings across harness-authored surfaces (system, system/developer messages, tool and JSON Schema descriptions/titles, and user <system-reminder> spans) on routes matching model_prefixes (or all models when empty) (match → replacement). Merges with built-in OMP presets; matching keys override built-ins case-insensitively and empty string values strip matched text.",
		},
	}
}
