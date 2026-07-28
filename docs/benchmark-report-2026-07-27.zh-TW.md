# context-compactor 正式 Benchmark 報告（2026-07-27）

## 1. 執行結果（正式版）

本次正式驗證採用：
- 模型：`gpt-5.6-sol`
- Codex CLI：`0.145.0`
- Reasoning：`high`
- model revision：`unavailable`
- sampling seed status：`unsupported`
- 矩陣：`formal`、`endurance`
- 5 個種子 × 3 個 scenario × 4 個 mode
- `manifest.complete=true`
- artifact：`benchmark-results\codex-gpt-5.6-sol-high-2026-07-27-v2.json`
- SHA-256：`A32129B02DCC8559BA8442690F0249A0EB70A53EC7B0668FF03E9F35C9DD97E6`

總量：
- `cases=120`
- `checkpoints=790`
- `primary=790`
- `diagnostic=0`
- `foreground total calls=790`
- `case_failures=0`
- `model_gate_failures=0`
- `deterministic_failures=0`
- `task_success_gap_failures=0`
- `model_not_evaluated=0`
- `token_gate_failures=0`

Gate 結果皆 pass：
- `token_gate_status=pass`
- `deterministic_gate_status=pass`
- `task_success_gate_status=pass`
- `model_gate_status=pass`
- `overall_status=pass`

報告列出的 13 項 Gates 皆 pass，包含：
- `critical_requirement_recall=0`
- `current_focus_and_active_task=0`
- `correct_next_action=0`

## 2. v1 baseline（fail）與 v2 最終（pass）差異

- v1（`benchmark-results\codex-gpt-5.6-sol-high-2026-07-27.json`）
  - SHA：`FD4DD076F3DF1990F50E0B8E45D8D6BDA79975963A2410E959657071D735223D`
  - `manifest.complete=true`
  - `official total calls=1568`（`primary=790`、`diagnostic=778`）
  - `overall_status=fail`
  - `model_gate_failures=187`
  - `critical_requirement_recall=65`、`current_focus_and_active_task=122`
  - `overall token/deterministic/task gates: pass`
- `model gate: fail`
  - foreground total：
    - `input_tokens=28,798,512`
    - `cached_input_tokens=21,307,904`
    - `output_tokens=169,887`
    - `reasoning_output_tokens=48,843`

- v2 最終結果
  - `critical_requirement_recall` 與 `current_focus_and_active_task` 均降為 `0`
  - `total failures=0`
- 差異總覽：
  - `diagnostic calls: 778 → 0`
  - `overall status: fail → pass`
- `model gate failures: 187 → 0`
- `case failures: 0 → 0`

## 3. 導致 v1 fail 的問題與修正歷程（簡述）

v1 失敗集中在 `full_transcript` 的兩類判定：
- `critical_requirement_recall`：`65`
- `current_focus_and_active_task`：`122`

修正方向：
1. `active_requirement` 改為掃完整 transcript，追蹤 `establish/replace/superseded`，回傳「最新且未被取代」的 requirement。
   - Resume 不能只看後段；需要回溯到先前仍有效的 `establish`。
2. `current_focus` 改為複製最後一輪 `agent_response`，不得用最後 `user_input` 或自行摘要。
3. `next_action` 改為複製最後一輪 `tool_activities` 最後一項，不能猜測。

完成修正後第一次 v2 full run 已先清掉上述兩類 full_transcript 失敗；但 `summary_only` 在 `formal/resume/seed4/turn30` 尚有 1 筆 `current_focus` 失敗，接著再補上 summary 規則：
- `current_progress -> current_focus` 精確對映
- `last_verification -> next_action` 精確對映
並在 targeted smoke 先驗證 `7/7` pass，後續 full formal+endurance 5 seeds 全矩陣才全部降為 `0`。

## 4. Cache 與 adapter 行為

- 使用 cache 目錄：`.cache/codex-foreground-model-v2`
- cache key 已納入 prompt digest（`SHA256`），因此 `ADAPTER_VERSION` 保持 `v1`
- prompt digest 改變使受影響舊 entries 無法命中並成為 inert，沒有刪 cache；final cache 共有 `965` 檔
- final artifact 僅使用 `790` 筆目前 digest 對應的 primary outputs
- final 跑法：
  - `compaction`：`0`
  - `diagnostic`: `0`（status=not_applicable）
  - 未儲存 `rendered_input`
  - artifact/cache secret scan：`0`

## 5. self-test 與 smoke 測試紀錄

- Codex adapter self-test：pass
- OpenAI adapter self-test：pass
- py_compile（Codex/OpenAI adapter）：pass
- benchmark 窄測：pass
- `go test ./...`：pass
- `go vet ./...`：pass
- `git diff --check`：pass

full_transcript smoke：
- `formal / seed1 / 3 scenarios`
- `3 cases / 27 checkpoints`
- `critical_requirement_recall=0`、`current_focus_and_active_task=0`
- `model_not_evaluated=0`
- `manifest` 未完整，故 `overall_status=not_evaluated`

summary targeted smoke（修正後）：
- `formal / resume / seed4 / summary_only`
- `1 case / 7 checkpoints`
- `all model checks=0 failure`
- `model_not_evaluated=0`
- `manifest` 未完整，`overall_status=not_evaluated`

## 6. Counter 與實際 token 節省

公式維持不變：
- `saved = full_input - input`
- `counter_% = saved / full_input * 100`
- `counter` 是 rendered variable context 的 UTF-8 bytes conservative 上界，非模型 host tokenizer。
- `cached_input_tokens` 是 `input_tokens` 的子集，不能直接互加。

v2 token accounting（final）：
- foreground total：`calls=790`
- `input_tokens=14,407,835`
- `cached_input_tokens=9,688,832`
- `output_tokens=57,930`
- `reasoning_output_tokens=7,456`
- `compaction calls=0`
- `diagnostic calls=0`（not_applicable）

拆分樣本（僅供對照，`fixed` 與 `event` 不可相加）：
- fixed：`calls=420`, `input=7,748,739`, `cached=5,063,680`, `output=29,338`, `reasoning=2,737`
- event：`calls=390`, `input=7,016,477`, `cached=4,850,432`, `output=29,998`, `reasoning=4,939`

## 7. 固定輪次節省中位數（每 matrix/turn/mode，跨 15 個 scenario×seed）

### Formal

| turn | strict counter | strict actual（input median / paired saved / paired%） | balanced counter | balanced actual（input median / paired saved / paired%） |
|---|---|---|---|---|
| 10 | `1393 / 62.75%` | `17473 / 518 / 2.88%` | `1263 / 56.89%` | `17499 / 492 / 2.74%` |
| 30 | `5813 / 87.55%` | `17477 / 1798 / 9.33%` | `5683 / 85.59%` | `17499 / 1776 / 9.22%` |
| 50 | `10380 / 92.60%` | `17473 / 3049 / 14.86%` | `10250 / 91.44%` | `17497 / 3027 / 14.75%` |
| 60 | `12620 / 93.83%` | `17473 / 3661 / 17.32%` | `12490 / 92.86%` | `17497 / 3639 / 17.22%` |

### Endurance

| turn | strict counter | strict actual（input median / paired saved / paired%） | balanced counter | balanced actual（input median / paired saved / paired%） |
|---|---|---|---|---|
| 60 | `12620 / 93.83%` | `17472 / 3661 / 17.32%` | `12490 / 92.86%` | `17498 / 3639 / 17.22%` |
| 90 | `19340 / 95.88%` | `17474 / 5491 / 23.91%` | `19210 / 95.24%` | `17498 / 5467 / 23.81%` |
| 120 | `26119 / 96.91%` | `17474 / 7321 / 29.53%` | `25989 / 96.42%` | `17498 / 7299 / 29.44%` |

## 8. 為何 strict 比 balanced 更省

`strict` 不保留 source evidence，只保留「必要的指令與狀態欄位」，`balanced` 則保留 evidence，因此 strict 在同場景下可得到更高節省。
在此 benchmark 內，兩者的 model quality checks 均為 `0 failures`。

## 9. Release 判定

依上列 v2 final artifact，正式矩陣已達 pass，可作為正式 release 判定依據。
限制如下，不可誤稱「完全可重現」：
- `model_revision=unavailable`
- `sampling_seed_status=unsupported`

## 10. 驗證命令參考

```powershell
Get-FileHash benchmark-results\codex-gpt-5.6-sol-high-2026-07-27-v2.json -Algorithm SHA256
```
