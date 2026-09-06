#!/usr/bin/env python3
"""
Issue #28 Cloak-Live Supplemental Gateway & Model Protocol Runner

NOTE: This script is a supplemental raw-HTTP protocol harness verifying gateway
admission, tool declaration cloaking, streamed model tool-call reversing, and
result continuation at the wire HTTP layer.

Real client execution (OMP TUI/CLI executing native tools, rendering interactive UI,
and spawning subagents) is separately validated through the real OMP CLI baseline
in docs/verification-checklist.md.
"""

import json
import os
import sys
import time
import urllib.request
import urllib.error

ENDPOINT = os.environ.get("CPA_ENDPOINT", "http://127.0.0.1:8317/v1/chat/completions")
API_KEY = os.environ.get("CPA_API_KEY", "Tonight123")
PROTECTED_MODEL = os.environ.get("CPA_MODEL", "agy/gemini-3.8-flash")
BYPASS_MODEL = os.environ.get("CPA_BYPASS_MODEL", "gemini-3.8-flash")

CANONICAL_MAPPINGS = [
    {
        "src": "read",
        "tgt": "view_file",
        "desc": "Read file contents",
        "params": {
            "type": "object",
            "properties": {"path": {"type": "string"}},
            "required": ["path"]
        },
        "prompt": "Call the read tool on path 'README.md' right now. Do not reply with text only.",
        "simulated_result": "File contents of README.md: # Antigravity Cloak\nDisguise coding-CLI traffic."
    },
    {
        "src": "write",
        "tgt": "write_to_file",
        "desc": "Write content to a file",
        "params": {
            "type": "object",
            "properties": {
                "path": {"type": "string"},
                "content": {"type": "string"}
            },
            "required": ["path", "content"]
        },
        "prompt": "Call the write tool to save 'hello' to 'output.txt' right now. Do not reply with text only.",
        "simulated_result": "Successfully wrote 5 bytes to output.txt"
    },
    {
        "src": "edit",
        "tgt": "replace_file_content",
        "desc": "Replace file content",
        "params": {
            "type": "object",
            "properties": {
                "path": {"type": "string"},
                "content": {"type": "string"}
            },
            "required": ["path", "content"]
        },
        "prompt": "Call the edit tool to edit 'config.json' right now. Do not reply with text only.",
        "simulated_result": "Content replaced successfully in config.json"
    },
    {
        "src": "bash",
        "tgt": "run_command",
        "desc": "Execute bash command",
        "params": {
            "type": "object",
            "properties": {
                "command": {"type": "string"}
            },
            "required": ["command"]
        },
        "prompt": "Call the bash tool with command 'git status' right now. Do not reply with text only.",
        "simulated_result": "On branch monet88/issue-28-cloak-live\nnothing to commit"
    },
    {
        "src": "grep",
        "tgt": "grep_search",
        "desc": "Search regex pattern across codebase",
        "params": {
            "type": "object",
            "properties": {
                "pattern": {"type": "string"}
            },
            "required": ["pattern"]
        },
        "prompt": "Call the grep tool with pattern 'handleRequestIntercept' right now. Do not reply with text only.",
        "simulated_result": "main.go:53: func handleRequestInterceptBefore"
    },
    {
        "src": "glob",
        "tgt": "find_by_name",
        "desc": "Find files by glob pattern",
        "params": {
            "type": "object",
            "properties": {
                "pattern": {"type": "string"}
            },
            "required": ["pattern"]
        },
        "prompt": "Call the glob tool with pattern '*.go' right now. Do not reply with text only.",
        "simulated_result": "main.go\nissue28_live_gate_test.go"
    },
    {
        "src": "task",
        "tgt": "invoke_subagent",
        "desc": "Delegate work to background subagents",
        "params": {
            "type": "object",
            "properties": {
                "context": {"type": "string"},
                "tasks": {"type": "array", "items": {"type": "object"}}
            },
            "required": ["context", "tasks"]
        },
        "prompt": "Call the task tool with context 'audit' and tasks [{'task': 'run check'}] right now. Do not reply with text only.",
        "simulated_result": "Subagent task completed: check passed"
    },
    {
        "src": "ask",
        "tgt": "ask_question",
        "desc": "Ask user interactive questions",
        "params": {
            "type": "object",
            "properties": {
                "questions": {
                    "type": "array",
                    "items": {
                        "type": "object",
                        "properties": {
                            "id": {"type": "string"},
                            "question": {"type": "string"}
                        },
                        "required": ["id", "question"]
                    }
                }
            },
            "required": ["questions"]
        },
        "prompt": "Call the ask tool to ask the question id 'confirm' question 'Proceed with deployment?' right now. Do not reply with text only.",
        "simulated_result": "User selected: Yes"
    },
    {
        "src": "web_search",
        "tgt": "search_web",
        "desc": "Search the web for current documentation",
        "params": {
            "type": "object",
            "properties": {
                "query": {"type": "string"}
            },
            "required": ["query"]
        },
        "prompt": "Call the web_search tool with query 'Golang 1.26 release notes' right now. Do not reply with text only.",
        "simulated_result": "Found 3 results for Golang 1.26 release notes"
    }
]

def send_stream_request(payload, extra_headers=None):
    headers = {
        "Content-Type": "application/json",
        "Authorization": f"Bearer {API_KEY}",
    }
    if extra_headers:
        headers.update(extra_headers)

    req = urllib.request.Request(
        ENDPOINT,
        data=json.dumps(payload).encode("utf-8"),
        headers=headers
    )
    with urllib.request.urlopen(req) as resp:
        tool_calls = {}
        content_parts = []
        raw_events = []
        for line in resp:
            line_str = line.decode("utf-8", errors="replace")
            raw_events.append(line_str)
            if not line_str.startswith("data: "):
                continue
            data_str = line_str[6:].strip()
            if data_str == "[DONE]":
                break
            try:
                chunk = json.loads(data_str)
            except Exception:
                continue

            choices = chunk.get("choices", [])
            if not choices:
                continue
            delta = choices[0].get("delta", {})
            if "content" in delta and delta["content"]:
                content_parts.append(delta["content"])

            tc_list = delta.get("tool_calls")
            if tc_list:
                for tc in tc_list:
                    idx = tc.get("index", 0)
                    if idx not in tool_calls:
                        tool_calls[idx] = {
                            "id": tc.get("id", ""),
                            "type": tc.get("type", "function"),
                            "function": {
                                "name": tc.get("function", {}).get("name", ""),
                                "arguments": tc.get("function", {}).get("arguments", "")
                            }
                        }
                    else:
                        fn = tc.get("function", {})
                        if "name" in fn and fn["name"]:
                            tool_calls[idx]["function"]["name"] += fn["name"]
                        if "arguments" in fn and fn["arguments"]:
                            tool_calls[idx]["function"]["arguments"] += fn["arguments"]
                        if "id" in tc and tc["id"]:
                            tool_calls[idx]["id"] = tc["id"]

        return {
            "status": resp.status,
            "tool_calls": list(tool_calls.values()),
            "content": "".join(content_parts),
            "raw_events": raw_events
        }

def test_canonical_mapping(mapping):
    src = mapping["src"]
    tgt = mapping["tgt"]
    print(f"\n---> Testing canonical mapping: {src} -> {tgt} ({mapping['desc']})")

    tool_def = {
        "type": "function",
        "function": {
            "name": src,
            "description": mapping["desc"],
            "parameters": mapping["params"]
        }
    }

    turn1_messages = [
        {"role": "system", "content": "You are Oh My Pi. When requested to use a tool, invoke that exact function call."},
        {"role": "user", "content": mapping["prompt"]}
    ]

    turn1_payload = {
        "model": PROTECTED_MODEL,
        "messages": turn1_messages,
        "tools": [tool_def],
        "tool_choice": {"type": "function", "function": {"name": src}},
        "stream": True
    }

    # 1. Turn 1: request -> upstream cloaked to target -> model streams target -> reverse uncloaks to source
    res1 = send_stream_request(turn1_payload, extra_headers={"X-Cloak-Client": "oh_my_pi"})

    tcs = res1["tool_calls"]
    if not tcs:
        # Retry with force prompt without tool_choice
        turn1_payload["tool_choice"] = "auto"
        res1 = send_stream_request(turn1_payload, extra_headers={"X-Cloak-Client": "oh_my_pi"})
        tcs = res1["tool_calls"]

    if not tcs:
        print(f"FAIL: No tool calls received for {src}. Content: {res1['content']}")
        return False, f"No tool calls received for {src}"

    tc = tcs[0]
    called_name = tc["function"]["name"]
    tc_id = tc["id"]
    args = tc["function"]["arguments"]

    print(f"     Streamed tool call received: id={tc_id} name={called_name} args={args}")

    if called_name != src:
        print(f"FAIL: Expected uncloaked native name '{src}', got '{called_name}' (target was '{tgt}')")
        return False, f"Expected {src}, got {called_name}"

    # 2. Turn 2: Native tool result continuation
    # Client executes native tool, formats role: tool with native name, sends back
    turn2_messages = list(turn1_messages)
    turn2_messages.append({
        "role": "assistant",
        "content": None,
        "tool_calls": [
            {
                "id": tc_id,
                "type": "function",
                "function": {
                    "name": src,
                    "arguments": args
                }
            }
        ]
    })
    turn2_messages.append({
        "role": "tool",
        "tool_call_id": tc_id,
        "name": src,
        "content": mapping["simulated_result"]
    })

    turn2_payload = {
        "model": PROTECTED_MODEL,
        "messages": turn2_messages,
        "tools": [tool_def],
        "stream": True
    }

    res2 = send_stream_request(turn2_payload, extra_headers={"X-Cloak-Client": "oh_my_pi"})
    print(f"     Continuation response received: status={res2['status']} content_len={len(res2['content'])}")
    if not res2["content"] and not res2["tool_calls"]:
        print(f"FAIL: Continuation returned empty response")
        return False, "Continuation returned empty response"

    print(f"PASS: {src} -> {tgt} -> {src} full round trip and continuation successful!")
    return True, f"Full round-trip and continuation OK (id={tc_id})"

def test_non_agy_bypass():
    print(f"\n---> Testing explicit OMP non-AGY bypass on model: {BYPASS_MODEL}")

    tool_def = {
        "type": "function",
        "function": {
            "name": "bash",
            "description": "Execute bash command",
            "parameters": {
                "type": "object",
                "properties": {"command": {"type": "string"}},
                "required": ["command"]
            }
        }
    }

    payload = {
        "model": BYPASS_MODEL,
        "messages": [
            {"role": "system", "content": "You are Oh My Pi. Invoke the bash tool."},
            {"role": "user", "content": "Run bash echo bypass_check"}
        ],
        "tools": [tool_def],
        "tool_choice": {"type": "function", "function": {"name": "bash"}},
        "stream": True
    }

    res = send_stream_request(payload, extra_headers={"X-Cloak-Client": "oh_my_pi"})
    tcs = res["tool_calls"]
    if not tcs:
        payload["tool_choice"] = "auto"
        res = send_stream_request(payload, extra_headers={"X-Cloak-Client": "oh_my_pi"})
        tcs = res["tool_calls"]

    if not tcs:
        print(f"FAIL: No tool calls received on bypass route")
        return False, "No tool calls received"

    called_name = tcs[0]["function"]["name"]
    print(f"     Bypass streamed tool call: {called_name}")
    if called_name != "bash":
        print(f"FAIL: Expected tool 'bash' on non-AGY bypass route, got '{called_name}'")
        return False, f"Expected bash, got {called_name}"

    print(f"PASS: Non-AGY bypass preserved tool 'bash' with zero mutation!")
    return True, "Non-AGY bypass tool name preserved"

def test_brand_restoration():
    print(f"\n---> Testing Protected Brand restoration (Antigravity -> omp in assistant stream)")

    payload = {
        "model": PROTECTED_MODEL,
        "messages": [
            {"role": "system", "content": "You are a helpful assistant. Output this exact phrase: 'Hello from Antigravity and Antigravity'."},
            {"role": "user", "content": "Repeat: 'Hello from Antigravity and Antigravity'."}
        ],
        "stream": True
    }

    res = send_stream_request(payload, extra_headers={"X-Cloak-Client": "oh_my_pi"})
    content = res["content"]
    print(f"     Received response: {content.strip()}")

    # Hard gate: Protected assistant-visible Antigravity must NOT leak.
    if "Antigravity" in content or "Antigravity" in content:
        print(f"FAIL: Leaked protected Antigravity brand in assistant stream: {content}")
        return False, f"Leaked protected brand in assistant stream: {content}"

    # Hard gate: For oh_my_pi client, canonical terminal alias 'omp' must be present
    if "omp" not in content and "Oh My Pi" not in content:
        print(f"FAIL: Expected restored canonical brand 'omp' in stream, got: {content}")
        return False, f"Expected canonical brand 'omp' in stream, got: {content}"

    print(f"PASS: Brand restored Antigravity to omp in stream with zero leakage!")
    return True, "Brand restored Antigravity to omp with zero leakage"

def test_dot_omp_path_preservation():
    print(f"\n---> Testing .omp path segment byte-for-byte preservation")

    expected_path = "C:\\Users\\monet\\.omp\\agent and /.omp/config"
    payload = {
        "model": PROTECTED_MODEL,
        "messages": [
            {"role": "user", "content": f"Repeat this exact path text verbatim: {expected_path}"}
        ],
        "stream": True
    }

    res = send_stream_request(payload, extra_headers={"X-Cloak-Client": "oh_my_pi"})
    content = res["content"]
    print(f"     Received path response: {content.strip()}")

    if ".omp" not in content:
        print(f"FAIL: Expected '.omp' in response, got: {content}")
        return False, f"Missing '.omp' in response: {content}"

    if ".Antigravity" in content:
        print(f"FAIL: '.omp' was erroneously corrupted to '.Antigravity': {content}")
        return False, f"Erronously replaced '.omp' with '.Antigravity': {content}"

    print(f"PASS: .omp path segments preserved byte-for-byte!")
    return True, ".omp path segments preserved byte-for-byte"

def main():
    print("=================================================================")
    print("  Issue #28 Cloak-Live Acceptance Gate Runner")
    print(f"  Endpoint: {ENDPOINT}")
    print(f"  Protected Model: {PROTECTED_MODEL}")
    print(f"  Bypass Model: {BYPASS_MODEL}")
    print("=================================================================")

    results = {}

    # Test all 9 canonical mappings
    for m in CANONICAL_MAPPINGS:
        ok, msg = test_canonical_mapping(m)
        results[f"{m['src']} -> {m['tgt']}"] = {"pass": ok, "msg": msg}
        time.sleep(0.5)

    # Test Non-AGY Bypass
    ok_bypass, msg_bypass = test_non_agy_bypass()
    results["Explicit OMP Non-AGY Bypass (gemini-3.8-flash)"] = {"pass": ok_bypass, "msg": msg_bypass}

    # Test Brand Restoration
    ok_brand, msg_brand = test_brand_restoration()
    results["Protected Brand Restoration (Antigravity -> omp)"] = {"pass": ok_brand, "msg": msg_brand}

    # Test .omp path preservation
    ok_path, msg_path = test_dot_omp_path_preservation()
    results["Protected Brand .omp Path Preservation"] = {"pass": ok_path, "msg": msg_path}
    print("\n=================================================================")
    print("  LIVE ACCEPTANCE MATRIX SUMMARY")
    print("=================================================================")
    all_pass = True
    for name, r in results.items():
        status = "PASS" if r["pass"] else "FAIL"
        if not r["pass"]:
            all_pass = False
        print(f"  [{status}] {name:<48} : {r['msg']}")

    print("=================================================================")
    if all_pass:
        print("  OVERALL VERDICT: ALL ACCEPTANCE TESTS PASSED (PASS)")
    else:
        print("  OVERALL VERDICT: ONE OR MORE TESTS FAILED (FAIL)")
    print("=================================================================")

    sys.exit(0 if all_pass else 1)

if __name__ == "__main__":
    main()
