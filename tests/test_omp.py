import os
import urllib.request
import json
import traceback

def test_omp_chat_completions():
    # Oh My Pi sending tools to /v1/chat/completions with canonical Safe Mapping Set
    endpoint = os.environ.get("CPA_ENDPOINT", "http://127.0.0.1:8317/v1/chat/completions")
    api_key = os.environ.get("CPA_API_KEY", "Tonight123@")
    model = os.environ.get("CPA_MODEL", "agy/gemini-3.8-flash")

    payload = {
        "model": model,
        "messages": [
            {
                "role": "system",
                "content": "You are Oh My Pi, an interactive coding assistant."
            },
            {
                "role": "user",
                "content": "List files in the current directory using bash or glob"
            }
        ],
        "tools": [
            {
                "type": "function",
                "function": {
                    "name": "read",
                    "description": "Read file contents",
                    "parameters": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}
                }
            },
            {
                "type": "function",
                "function": {
                    "name": "write",
                    "description": "Write file contents",
                    "parameters": {"type": "object", "properties": {"path": {"type": "string"}, "content": {"type": "string"}}, "required": ["path", "content"]}
                }
            },
            {
                "type": "function",
                "function": {
                    "name": "edit",
                    "description": "Edit file",
                    "parameters": {"type": "object", "properties": {"path": {"type": "string"}}, "required": ["path"]}
                }
            },
            {
                "type": "function",
                "function": {
                    "name": "bash",
                    "description": "Execute bash command",
                    "parameters": {"type": "object", "properties": {"command": {"type": "string"}}, "required": ["command"]}
                }
            },
            {
                "type": "function",
                "function": {
                    "name": "grep",
                    "description": "Search code",
                    "parameters": {"type": "object", "properties": {"pattern": {"type": "string"}}, "required": ["pattern"]}
                }
            },
            {
                "type": "function",
                "function": {
                    "name": "glob",
                    "description": "Find files by pattern (maps to find_by_name)",
                    "parameters": {"type": "object", "properties": {"pattern": {"type": "string"}}, "required": ["pattern"]}
                }
            },
            {
                "type": "function",
                "function": {
                    "name": "task",
                    "description": "Run subtask",
                    "parameters": {"type": "object", "properties": {"context": {"type": "string"}, "tasks": {"type": "array"}}, "required": ["context", "tasks"]}
                }
            },
            {
                "type": "function",
                "function": {
                    "name": "ask",
                    "description": "Ask question",
                    "parameters": {"type": "object", "properties": {"questions": {"type": "array"}}, "required": ["questions"]}
                }
            },
            {
                "type": "function",
                "function": {
                    "name": "web_search",
                    "description": "Search the web",
                    "parameters": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}
                }
            }
        ],
        "stream": True
    }
    req = urllib.request.Request(
        endpoint,
        data=json.dumps(payload).encode("utf-8"),
        headers={
            "Content-Type": "application/json",
            "Authorization": f"Bearer {api_key}",
            "X-Cloak-Client": "oh_my_pi"
        }
    )
    print(f"=== Sending Oh My Pi stream request to {endpoint} ===")
    try:
        with urllib.request.urlopen(req) as resp:
            print("STATUS:", resp.status)
            full_chunks = []
            for line in resp:
                text = line.decode("utf-8", errors="replace")
                full_chunks.append(text)
                print(text, end="")
    except urllib.error.HTTPError as e:
        print("HTTPError:", e.code, e.read().decode("utf-8", errors="replace"))
    except Exception as e:
        traceback.print_exc()

if __name__ == "__main__":
    test_omp_chat_completions()
