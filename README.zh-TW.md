# context-compactor

[English](README.md)

`context-compactor` 是一個早期開發中的 local-first coding agent 上下文壓縮工具。
它能從一般自然語言保留任務目標、限制、決策與實作狀態，prompt retention
則維持長度受限、敏感資訊遮罩、本機保存且有期限。

> 狀態：protocol、local SQLite journal、deterministic reducer/compiler、
> Codex 與 Claude hook runtime、背景自然語言記憶判斷，以及 detached capsule
> publication 已完成實作。專案已有公開 Windows amd64 release，且原始碼安裝仍
> 可供專案本地 Codex/Claude 使用。

## 設計目標

- 在長 session、compact 與 resume 後保留 critical requirements 與否定限制。
- Repository 檔案與使用者明確指令是事實來源；壓縮記憶是可重建的衍生檢視。
- 保存結構化的記憶增量操作，不讓模型直接覆寫整份狀態文件。
- Production 只使用 standard privacy：prompt job 先遮罩疑似 secret，最多保留
  8,000 個 Unicode 字元、七天、每個 repository 500 筆，且只存在本機。
- 使用同一套 Go codebase 支援 Windows、macOS 與 Linux。
- 以單一 60 輪流程，在第 10、30、50、60 輪驗證 token 降幅與任務恢復品質。

## 目前範圍

Repository 目前包含 `context-compactor/v1` protocol 型別、deterministic validation、
每個 repository 各自使用的 SQLite event journal、deterministic reducer/compiler、
Codex／Claude hook adapters 與可執行的本機 runtime。User-prompt hook 不會等待
model；detached repository worker 會透過目前已登入的 host CLI，取得 `no_change`
或 typed memory update。通過 deterministic validation 的 operations 只有在 memory
確實改變時才會重建 view 並排入 capsule publication，下一個支援的 hook 即可注入
bounded context。行為規格請參閱 [SPEC.md](SPEC.md)，安裝與管理方式請參考本篇
「安裝」章節。

## 開發

需求：

- Go 1.26 或更新版本

執行目前的驗證：

```sh
go test ./...
go vet ./...
```

行為契約與 benchmark Gate 請見 [SPEC.md](SPEC.md)。

執行含 foreground model 檢查的正式 benchmark：

```sh
export OPENAI_API_KEY="..."
docker run --rm -e OPENAI_API_KEY -v "$PWD:/workspace" -w /workspace \
  -e GOCACHE=/workspace/.cache/go-build \
  -e GOMODCACHE=/workspace/.cache/go-mod \
  golang:1.26 go run ./cmd/context-compactor benchmark --matrix formal \
  --model-command /usr/bin/python3 \
  --model-arg /workspace/scripts/foreground_model_openai.py
```

可用 `OPENAI_FOREGROUND_MODEL` 覆寫預設 foreground model。未設定 model command
時，benchmark 仍會回報 token 與 deterministic gate，但 model-dependent gate 會是
`not_evaluated`。

## 安裝

### 從 release 安裝或更新（Windows amd64）

在 Windows PowerShell 5.1+ 執行以下指令，即可安裝 latest release 或更新既有的
managed installation：

```powershell
irm https://raw.githubusercontent.com/ivyliu1201/context-compactor/main/scripts/install-release.ps1 | iex
```

安裝器會從 GitHub latest release 下載 executable 與 `checksums.txt`、驗證
SHA-256，並依序執行 `self-check`、`install`、`status`、`doctor`。

- Cyan：進行中。
- Green：成功。
- Yellow：需處理。
- Red：失敗。
- 不會向一般使用者直接顯示原始 CLI JSON。
- 未指定 `-ProjectRoot` 時，Codex config 與 install manifest 會放在使用者
  `HOME`，且 Hook command 不會固定帶 `--project-root`。
- 專案 root 依每次 Codex Hook payload 的 `cwd` 決定。
- 指定 `-ProjectRoot` 時，仍保留 project-local 固定 root 行為。
- Executable 會安裝到 `%LOCALAPPDATA%\context-compactor`。
- Codex 可能仍需透過 `/hooks` 完成 review 或 trust。

### 從 source 安裝（專案本地，需 Docker）

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -ProjectRoot .
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -ProjectRoot . -AgentHost claude
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -ProjectRoot . -AgentHost all
```

### 移除全域 Codex Hook

```powershell
$m = Get-Content (Join-Path $HOME ".context-compactor\install.json") -Raw | ConvertFrom-Json; & $m.hosts.codex.executable uninstall --host codex --project-root $HOME
```

此 CLI 移除僅限本安裝器管理的 Hook 定義與 manifest entry，不會刪除已安裝的
executable。

## 自然語言記憶

不需要特殊前綴，也不需要使用「記住」句型。例如：

```text
這個專案的 timestamp 必須使用 UTC。
先完成 detached worker integration，不要更動 benchmark flow。
先前的 Windows-only 限制已經解除。
```

Hook 會保存長度受限且已遮罩疑似 secret 的 extraction job，並自動啟動既有
detached worker。背景 model 可以提出 typed goal、acceptance criterion、
constraint、decision、blocker、question、task、file 或已驗證的 test result；
任何內容進入 memory 前都會通過 deterministic validation。只要求解釋、翻譯、
一般知識，或其他不影響專案的 prompt，會完成為 `no_change`，也不會發布新 capsule。

Codex 預設透過已登入的 `codex` CLI，routine 使用 `gpt-5.4-mini`、repair 使用
`gpt-5.4`；Claude 預設透過已登入的 `claude` CLI，分別使用 `haiku` 與
`sonnet`。可用下列環境變數覆寫 executable、model 與 Codex reasoning effort：

- `CONTEXT_COMPACTOR_CODEX_COMMAND`
- `CONTEXT_COMPACTOR_CLAUDE_COMMAND`
- `CONTEXT_COMPACTOR_CODEX_ROUTINE_MODEL`
- `CONTEXT_COMPACTOR_CODEX_REPAIR_MODEL`
- `CONTEXT_COMPACTOR_CLAUDE_ROUTINE_MODEL`
- `CONTEXT_COMPACTOR_CLAUDE_REPAIR_MODEL`
- `CONTEXT_COMPACTOR_CODEX_REASONING`
- `CONTEXT_COMPACTOR_USE_ANTHROPIC_API_KEY=1`

## 隱私模式

Production 只提供 `standard` policy；為了相容既有資料，它在 version 1 wire
仍使用 `balanced` 值。舊的 `strict` 與 `audit` 資料仍可讀，但不能用於新的
production run。受限的 prompt field 只存在 extraction-job table，不會直接被
render；只有通過驗證的 durable facts 與短 evidence 可能進入 compiled context。
Retention 與 model provider 邊界請見 [docs/PRIVACY.md](docs/PRIVACY.md)。

## 授權

本專案採用 [Apache License 2.0](LICENSE)。
