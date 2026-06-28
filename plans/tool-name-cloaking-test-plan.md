# Kế hoạch kiểm thử tính năng Tool Name Cloaking

Tài liệu này hướng dẫn cách Đại Ca thực hiện kiểm thử thực tế bằng Claude Code và Codex, đồng thời cung cấp các lệnh check log cho AI ở session tiếp theo để xác minh tính đúng đắn.

## 1. Các bước thực hiện kiểm thử (Đại Ca thực hiện)

### Kiểm thử với Claude Code:
1. Thiết lập base URL hướng về proxy và API key trong PowerShell:
   ```powershell
   $env:CLAUDE_BASE_URL="http://localhost:8333/v1"
   $env:CLAUDE_API_KEY="sk-k3skgrBK1nw8fExaR"
   ```
2. Khởi động Claude Code:
   ```powershell
   claude
   ```
3. Gửi một số request yêu cầu Claude Code thực hiện hành động trên máy (ví dụ: chạy lệnh `git status`, đọc file cấu hình, tìm kiếm file, hoặc hỏi đáp thông thường).

---

### Kiểm thử với Codex:
1. Thiết lập base URL hướng về proxy và API key trong PowerShell:
   ```powershell
   $env:OPENAI_BASE_URL="http://localhost:8333/v1"
   $env:OPENAI_API_KEY="sk-k3skgrBK1nw8fExaR"
   ```
2. Khởi chạy Codex và thực hiện gửi request gọi tool (như apply patch hoặc chạy lệnh shell).

---

## 2. Hướng dẫn Check Log cho AI (Session sau thực hiện)

Khi Đại Ca yêu cầu kiểm tra log, AI ở session mới cần thực hiện quét log của container `cli-proxy-api-origin` để tìm các sự kiện sau:

### Lệnh lấy log từ Docker:
```powershell
# Lấy log trong vòng 30 phút qua của container proxy
docker logs --since 30m cli-proxy-api-origin
```

### Các tiêu chí xác minh trong log:

1. **Khởi động và Nạp Plugin**:
   Log khởi động của container phải chứa dòng đăng ký thành công plugin phiên bản `0.1.0`:
   `pluginhost: plugin registered plugin_id=antigravity-coding-filter plugin_name=antigravity-coding-filter version=0.1.0`

2. **Yêu cầu chiều đi (Request Cloaking)**:
   - Proxy nhận dạng client là `claude_code` hoặc `codex`.
   - Các công cụ được che giấu thành công:
     - Cho Claude Code: `bash` -> `run_command`, `edit` -> `replace_file_content`, `read` -> `view_file`, `write` -> `write_to_file`.
     - Cho Codex: `shell_command` -> `run_command`, `apply_patch` -> `multi_replace_file_content`.

3. **Yêu cầu chiều về (Response Uncloaking)**:
   - Tool call từ LLM chứa `run_command` hoặc `replace_file_content` được uncloak ngược lại chuẩn xác thành `bash` hoặc `edit` tương ứng trước khi phản hồi về cho client.
   - Text hội thoại (prose) của mô hình không bị thay thế từ khóa bừa bãi.
