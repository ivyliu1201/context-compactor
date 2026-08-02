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
- Codex `SessionStart` 只匹配 `startup`，且不會建立工作或啟動模型 worker。

## Windows 一鍵安裝或更新

請先在要啟用 Hook 的 coding project 根目錄開啟 PowerShell。環境需要
Windows PowerShell 5.1+、Git、Python 3.9+，以及已登入的 Codex CLI。
第一次安裝與之後更新都執行同一條指令：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -Command "& ([scriptblock]::Create((Invoke-RestMethod 'https://raw.githubusercontent.com/ivyliu1201/context-compactor/v3.1.0/scripts/bootstrap.ps1')))"
```

這條指令會先從 `v3.1.0` release tag 載入固定版本的 bootstrap。bootstrap
再從本 repository 取得最新的公開穩定 Release，並檢查下載網址、release tag
與來源版本，再執行可重複呼叫的 source installer；完成後會清除暫存下載檔。
版本化來源與私有 venv 預設安裝到
`%LOCALAPPDATA%\context-compactor`。

看到回傳 JSON 包含 `"ok": true` 後，請在同一個專案目錄啟動 Codex。
若 PowerShell 阻擋 npm 的 `codex.ps1`，請改輸入 `codex.cmd`。Codex 若詢問
是否信任 project Hook，請確認信任。受管理的 `SessionStart` Hook 只會在
`startup` 執行；一般 prompt 由背景 memory worker 處理。

### Source tree 管理指令

若已 clone 或解壓縮 source tree，仍可直接使用原管理腳本。`install` 可安全
重複執行；較嚴格的 `update` 必須先有既存 source installation 與已安裝專案。

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action install -ProjectRoot . -AgentHost codex

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action update -ProjectRoot . -AgentHost codex

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action status -ProjectRoot . -AgentHost codex

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action doctor -ProjectRoot . -AgentHost codex

powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action uninstall -ProjectRoot . -AgentHost codex
```

source installer 支援 `AgentHost`：`codex`、`claude`、`all`。
install/update 只移除可辨識 V2 manifest 精確記錄的 V2 Hook handler，並保留
manifest、舊 `context.db`、專案狀態與無關的使用者 Hook。

若要改用外部 adapter，可加入以下 override：

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\install.ps1 -Action install -ProjectRoot . -AgentHost codex -ModelCommandJson '["python","C:\\path\\to\\model-adapter.py"]'
```

外部 adapter 必須實作 `context-compactor/model/v1`：

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
- 驗證結果：79 測試成功執行，含 2 個依環境條件 skip 的測試。
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
- Endurance 階段（60 輪）：通過，seed 同上；第 60 輪節省率為
  `81.97%`、`81.97%`、`82.15%`；第 45 與 60 輪累計節省率為
  `79.83%`–`79.91%`；Hook/background 最差為 `24.460ms/30.280ms`。
- 以上為確定性 benchmark 情境中實際觀測的 input-token 降幅，不保證每個
  live turn 或帳單成本都會得到相同比例。
- 通過 correctness、privacy、state-budget、failed-candidate-corruption 各項 gate。

## 授權

[Apache License 2.0](LICENSE)
