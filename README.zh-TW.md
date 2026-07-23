# context-compactor

[English](README.md)

`context-compactor` 是一個早期開發中的 local-first coding agent 上下文壓縮工具。
它的目標是在預設不保存完整 prompt 的情況下，保留任務目標、限制、決策與實作狀態。

> 狀態：已完成 protocol、本機 SQLite journal、deterministic reducer/compiler、
> Codex／Claude hook runtime 與 durable capsule refresh handoff；
> 已可從原始碼管理專案本地安裝，但目前尚無已發布的 binary。

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
context。Capsule refresh 會先持久寫入可恢復的 worker queue，不會只留在短生命週期
goroutine。安裝與發行流程仍列在 [TODO.md](TODO.md)。

## 開發

需求：

- Go 1.26 或更新版本

執行目前的驗證：

```sh
go test ./...
go vet ./...
```

行為契約與 benchmark Gate 請見 [SPEC.md](SPEC.md)。

建置並執行目前的 source-stage executable：

```sh
go build -o context-compactor ./cmd/context-compactor
context-compactor install --host all --project-root /path/to/repository
context-compactor status --host all --project-root /path/to/repository
context-compactor doctor --host all --project-root /path/to/repository
context-compactor refresh-worker --project-root /path/to/repository
context-compactor uninstall --host all --project-root /path/to/repository
```

Hook 只從標準輸入讀取一個 host payload，標準輸出只用來回傳 host JSON。Install
只會將 context-compactor 的五種 lifecycle hooks 合併進專案本地設定，並把精確
command 記錄在已被 gitignore 的 `.context-compactor/install.json`。Uninstall
只移除完全相符的受管理項目；若使用者曾修改該 hook，會停止而不猜測刪除。

Codex definition 會寫入 `.codex/hooks.json`。Codex 要求使用者透過 `/hooks`
審查並信任新增或變更的專案 hooks，因此 status 只會回報
`awaiting_manual_trust`，不會宣稱壓縮已啟用。Claude definition 會寫入
`.claude/settings.local.json`。Doctor 會實際執行安裝 binary 的 bounded
self-check，並驗證每一個受管理 definition；無法讀取的 host trust 或企業政策會
保留為 activation unknown。Refresh-worker 排程目前仍需手動設定。

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
