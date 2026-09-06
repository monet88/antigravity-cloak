import os
import urllib.request
import json
import traceback

def test_messages():
    base_url = os.environ.get("CPA_BASE_URL", "http://127.0.0.1:8317")
    api_key = os.environ.get("CPA_API_KEY", "Tonight123@")
    model = os.environ.get("CPA_MODEL", "agy/gemini-3.8-flash")

    payload = {
        "model": model,
        "messages": [{"role": "user", "content": "Run git status using Bash"}],
        "system": "You are Claude Code, Anthropic's official CLI.",
        "tools": [
            {"name": "Bash", "description": "Execute a bash command", "input_schema": {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]}},
            {"name": "Read", "description": "Read a file", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}},
            {"name": "Edit", "description": "Edit a file", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}},
            {"name": "Write", "description": "Write a file", "input_schema": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}}
        ],
        "stream": True,
        "max_tokens": 4096
    }
    req = urllib.request.Request(
        f"{base_url}/v1/messages",
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            "x-api-key": api_key,
            "Authorization": f"Bearer {api_key}",
            "anthropic-version": "2023-06-01"
        }
    )
    print(f"=== Testing {base_url}/v1/messages (Claude Code) ===")
    try:
        with urllib.request.urlopen(req) as resp:
            print("STATUS:", resp.status)
            for line in resp:
                print(line.decode("utf-8", errors="replace"), end="")
    except urllib.error.HTTPError as e:
        print("HTTPError:", e.code, e.read().decode("utf-8", errors="replace"))
    except Exception as e:
        traceback.print_exc()

def test_chat_completions():
    base_url = os.environ.get("CPA_BASE_URL", "http://127.0.0.1:8317")
    api_key = os.environ.get("CPA_API_KEY", "Tonight123@")
    model = os.environ.get("CPA_MODEL", "agy/gemini-3.8-flash")

    payload = {
        "model": model,
        "messages": [
            {"role": "system", "content": "You are OpenCode, an AI coding assistant."},
            {"role": "user", "content": "Run git status using bash"}
        ],
        "tools": [
            {"type": "function", "function": {"name": "bash", "description": "Execute bash", "parameters": {"type": "object", "properties": {"command": {"type": "string"}}}}},
            {"type": "function", "function": {"name": "read", "description": "Read file", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}},
            {"type": "function", "function": {"name": "edit", "description": "Edit file", "parameters": {"type": "object", "properties": {"path": {"type": "string"}}}}},
            {"type": "function", "function": {"name": "task", "description": "Task", "parameters": {"type": "object", "properties": {"prompt": {"type": "string"}}}}}
        ],
        "stream": True
    }
    req = urllib.request.Request(
        f"{base_url}/v1/chat/completions",
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {api_key}"
        }
    )
    print(f"\n=== Testing {base_url}/v1/chat/completions (OpenCode / Oh My Pi) ===")
    try:
        with urllib.request.urlopen(req) as resp:
            print("STATUS:", resp.status)
            for line in resp:
                print(line.decode("utf-8", errors="replace"), end="")
    except urllib.error.HTTPError as e:
        print("HTTPError:", e.code, e.read().decode("utf-8", errors="replace"))
    except Exception as e:
        traceback.print_exc()

if __name__ == "__main__":
    test_messages()
    test_chat_completions()
