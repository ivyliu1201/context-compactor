# context-compactor

[English](README.md)

`context-compactor` 是一個早期開發中的 local-first coding agent 上下文壓縮工具。
它的目標是在預設不保存完整 prompt 的情況下，保留任務目標、限制、決策與實作狀態。

> 狀態：protocol、local SQLite journal、deterministic reducer/compiler、
> Codex 與 Claude hook runtime，以及 durable capsule-refresh handoff 已完成實作。
> 專案已有公開 Windows amd64 release，且原始碼安裝仍可供專案本地
> Codex/Claude 使用。

## 設計目標

- 在長 session、compact 與 resume 後保留 critical requirements 與否定限制。
- Repository 檔案與使用者明確指令是事實來源；壓縮記憶是可重建的衍生檢視。
- 保存結構化的記憶增量操作，不讓模型直接覆寫整份狀態文件。
- 預設採 balanced privacy：不保存完整 prompt，只保存長度受限且已遮罩的證據片段，資料維持在本機。
- 使用同一套 Go codebase 支援 Windows、macOS 與 Linux。
- 以單一 60 輪流程，在第 10、30、50、60 輪驗證 token 降幅與任務恢復品質。

## 目前範圍

Repository 目前包含 `context-compactor/v1` protocol 型別、deterministic validation、
每個 repository 各自使用的 SQLite event journal、deterministic reducer/compiler、
Codex／Claude hook adapters 與可執行的本機 runtime。每次 hook 會在同一 transaction
內寫入通過驗證的 memory operations 並重建 materialized view，之後才輸出 bounded
context。Capsule refresh 已改為交由可恢復 worker 的 durable queue 處理，不再使用
短生命週期 goroutine。行為規格請參閱 [SPEC.md](SPEC.md)，安裝與管理方式請參考
本篇「安裝」章節。

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

### 從 release 安裝（Windows amd64）

在 Windows PowerShell 5.1+ 執行：

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

一般 prompt 文字維持 transient。內建 deterministic extractor 只保存明確指令，例如：

```text
[context-compactor] goal: 完成 bounded hook runtime。
[context-compactor] task: 驗證 durable refresh worker。
[context-compactor] resolve: record-id
```

支援的 record 名稱為 `goal`、`acceptance_criterion`、`constraint`、`decision`、
`blocker`、`question`、`task`、`file`、`test_result`；lifecycle 指令為
`resolve` 與 `expire`。

## 隱私模式

規劃中的模式：

- `strict`：只保存結構化事實，不保存證據文字。
- `balanced`：保存結構化事實與短小、已遮罩的證據片段；這是預設模式。
- `audit`：需要更完整追溯能力的使用者明確 opt-in 後才能啟用。

在預設設計中，完整 prompt 只作為暫時輸入，不是持久記憶。

## 授權

本專案採用 [Apache License 2.0](LICENSE)。
