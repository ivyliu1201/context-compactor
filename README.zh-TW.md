# context-compactor

[English](README.md)

`context-compactor` 是一個早期開發中的 local-first coding agent 上下文壓縮工具。
它的目標是在預設不保存完整 prompt 的情況下，保留任務目標、限制、決策與實作狀態。

> 狀態：已完成 protocol 基礎、本機 SQLite journal 與 deterministic memory reducer；
> 目前沒有已發布的執行檔或穩定 CLI。

## 設計目標

- 在長 session、compact 與 resume 後保留 critical requirements 與否定限制。
- Repository 檔案與使用者明確指令是事實來源；壓縮記憶是可重建的衍生檢視。
- 保存結構化的記憶增量操作，不讓模型直接覆寫整份狀態文件。
- 預設採 balanced privacy：不保存完整 prompt，只保存長度受限且已遮罩的證據片段，資料維持在本機。
- 使用同一套 Go codebase 支援 Windows、macOS 與 Linux。
- 以 10、30、50 輪同時驗證 token 降幅與任務恢復品質。

## 目前範圍

Repository 目前包含 `context-compactor/v1` protocol 型別、deterministic validation、
每個 repository 各自使用的本機 SQLite event journal，以及 deterministic memory
reducer。Reducer 會套用 lifecycle operations、標記 duplicate、分離記憶 priority 與
衝突 impact、偵測 advisory／blocking contradiction，並重建有 digest 驗證的
materialized view。Context selection、agent adapters 與發行流程仍列在
[TODO.md](TODO.md)。

## 開發

需求：

- Go 1.26 或更新版本

執行目前的驗證：

```sh
go test ./...
go vet ./...
```

行為契約與 benchmark Gate 請見 [SPEC.md](SPEC.md)。

## 隱私模式

規劃中的模式：

- `strict`：只保存結構化事實，不保存證據文字。
- `balanced`：保存結構化事實與短小、已遮罩的證據片段；這是預設模式。
- `audit`：需要更完整追溯能力的使用者明確 opt-in 後才能啟用。

在預設設計中，完整 prompt 只作為暫時輸入，不是持久記憶。

## 授權

本專案採用 [Apache License 2.0](LICENSE)。
