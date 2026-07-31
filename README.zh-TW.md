# context-compactor

[English](README.md)

`context-compactor` 是供 coding agents 使用的 local-first context 壓縮產品。

## 產品

- 僅提供 `standard` 路徑。
- Python 標準函式庫執行環境，Python 3.9+。
- 模型更新由獨立 Python worker 處理。
- 可讀狀態檔為 `.context-compactor/state.yaml`。
- 備援狀態檔為 `.context-compactor/state.backup.yaml`。
- 輕量 journal 為 `.context-compactor/events.sqlite`。

## Windows source installer

- 使用 `scripts/install.ps1`（PowerShell 5.1+，僅限 Windows）。
- 預設將來源複製到 `%LOCALAPPDATA%\context-compactor` 下的私有 venv。
- 支援 `install`、`update`、`status`、`doctor`、`uninstall`。
- 支援 `AgentHost`：`codex`、`claude`、`all`。
- `install` 與 `update` 需要 `-ModelCommandJson`。

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 `
  -Action install -ProjectRoot . -AgentHost all `
  -ModelCommandJson '["python","C:\\path\\to\\model-adapter.py"]'

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 `
  -Action update -ProjectRoot . -AgentHost all `
  -ModelCommandJson '["python","C:\\path\\to\\model-adapter.py"]'

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action status -ProjectRoot . -AgentHost all

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action doctor -ProjectRoot . -AgentHost all

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action uninstall -ProjectRoot . -AgentHost all
```

本產品不含內建 provider adapter。請使用外部 adapter，需實作 `context-compactor/model/v1`：

- 從 stdin 讀取一個 JSON request。
- 僅輸出一個 JSON response 到 stdout：
  - `{"outcome":"no_change"}`
  - `{"outcome":"updated","state":{...complete state schema...}}`

## 隱私

- `state.yaml` 不會保存原始 prompt、transcript、或 log。
- `events.sqlite` 僅可暫存遮罩後的 prompt，單筆上限 8000 字元。
- 成功後必須立即清除暫存 prompt，並最長保留 7 天。
- 已定義的 API-key、`Authorization: Bearer`、`Authorization: Basic`、bearer-token、password/secret assignment、private-key 樣式在寫入前皆需遮罩。
- 不保證可偵測到未知的新型機密格式。
- 詳見 [docs/PRIVACY.md](docs/PRIVACY.md)。

## 舊版資料庫遷移

```
python -m context_compactor migrate preview --project-root .
python -m context_compactor migrate apply --project-root .
```

遷移會讀取舊版 `.context-compactor/context.db`，不會修改也不會刪除原始資料庫。

## 驗證

- `python -B -m unittest discover -s tests`
- 驗證結果：68 測試成功執行，含 2 個 opt-in skip。
- 基準報告：[docs/benchmark-report-v3-2026-07-31.zh-TW.md](docs/benchmark-report-v3-2026-07-31.zh-TW.md)
- 使用已登入的 Codex CLI 實際執行，模型為 `gpt-5.6-sol` 且 reasoning effort 為 `high`。

```powershell
python -B scripts/benchmark_v3.py --stage release `
  --output benchmark-results/context-compactor-v3-standard-30turn-2026-07-31.json `
  --report docs/benchmark-report-v3-2026-07-31.zh-TW.md

python -B scripts/benchmark_v3.py --stage endurance `
  --output benchmark-results/context-compactor-v3-standard-60turn-2026-07-31.json `
  --report docs/benchmark-report-v3-2026-07-31.zh-TW.md `
  --stage1-result benchmark-results/context-compactor-v3-standard-30turn-2026-07-31.json
```

- Release 階段（30 輪）：通過，seed `17,29,43`，第 30 輪節省率 `68.66%`、`68.65%`、`68.66%`；整體中位數 `58.42%`；Hook/background 最差 `17.021ms/20.838ms`。
- Endurance 階段（60 輪）：通過，seed 同上，第 60 輪節省率 `81.97%`、`81.97%`、`82.15%`；整體中位數 `79.83%`；Hook/background 最差 `24.460ms/30.280ms`。
- 通過 correctness、privacy、state-budget、failed-candidate-corruption 各項 gate。

## 授權

[Apache License 2.0](LICENSE)
