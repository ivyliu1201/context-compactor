# Context Compactor Benchmark v3

- 結論：`pass`
- 情境：`combined-standard` / `standard`
- 階段：30 輪，seeds `[17, 29, 43]`
- Repository commit：`0a96239952d015516ff32180eada03c65c18d7ce`
- Codex CLI：`codex-cli 0.146.0`
- Foreground model：`gpt-5.6-sol` / `high`
- Token counter：`weighted-character-v1` （primary basis：`observed`）

## Token 結果

| Seed | Checkpoint | A input | B input | Saved | Reduction | Basis |
|---:|---:|---:|---:|---:|---:|---|
| 17 | 10 | 33946 | 21090 | 12856 | 37.87% | observed |
| 17 | 20 | 50794 | 21124 | 29670 | 58.41% | observed |
| 17 | 30 | 67417 | 21129 | 46288 | 68.66% | observed |
| 29 | 10 | 34158 | 21094 | 13064 | 38.25% | observed |
| 29 | 20 | 50794 | 21126 | 29668 | 58.41% | observed |
| 29 | 30 | 67415 | 21133 | 46282 | 68.65% | observed |
| 43 | 10 | 34156 | 21098 | 13058 | 38.23% | observed |
| 43 | 20 | 50796 | 21128 | 29668 | 58.41% | observed |
| 43 | 30 | 67417 | 21131 | 46286 | 68.66% | observed |

三 seed 中位數 reduction：`58.42%`；最差 seed：`17` / `58.37%`。

## Correctness Gates

| Gate | Result |
|---|---:|
| `active_goal_recall_100_percent` | pass |
| `all_structural_gates` | pass |
| `b_success_gap_at_most_3_points` | pass |
| `current_focus_100_percent` | pass |
| `negative_constraint_recall_100_percent` | pass |
| `next_action_100_percent` | pass |
| `resume_continuity_100_percent` | pass |
| `superseded_requirement_active_0` | pass |

## Privacy 與 bounded-state Gates

- Defined synthetic secret matches：`0`
- Limitation：Defined API-key, Bearer-token, password, and private-key patterns are checked; unknown secret formats are not guaranteed to be detected.
- State budget violations：`0`
- Failed-candidate corruptions：`0`

## Latency

- Hook p50 / worst：`9.690` / `17.021` ms
- Background p50 / worst：`14.405` / `20.838` ms

## Deviations

- None.

---

## 60 輪耐久測試

- 結論：`pass`
- 情境：`combined-standard` / `standard`
- 階段：60 輪，seeds `[17, 29, 43]`
- Repository commit：`14b6bc39a4f2875de7738ad8d6863b080e5837f8`
- Codex CLI：`codex-cli 0.146.0`
- Foreground model：`gpt-5.6-sol` / `high`
- Token counter：`weighted-character-v1` （primary basis：`observed`）

### Token 結果

| Seed | Checkpoint | A input | B input | Saved | Reduction | Basis |
|---:|---:|---:|---:|---:|---:|---|
| 17 | 45 | 92323 | 21128 | 71195 | 77.11% | observed |
| 17 | 60 | 117238 | 21141 | 96097 | 81.97% | observed |
| 29 | 45 | 92323 | 21128 | 71195 | 77.11% | observed |
| 29 | 60 | 117234 | 21137 | 96097 | 81.97% | observed |
| 43 | 45 | 92113 | 21126 | 70987 | 77.06% | observed |
| 43 | 60 | 117238 | 20927 | 96311 | 82.15% | observed |

三 seed 中位數 reduction：`79.83%`；最差 seed：`17` / `79.83%`。

### Correctness Gates

| Gate | Result |
|---|---:|
| `active_goal_recall_100_percent` | pass |
| `all_structural_gates` | pass |
| `b_success_gap_at_most_3_points` | pass |
| `current_focus_100_percent` | pass |
| `negative_constraint_recall_100_percent` | pass |
| `next_action_100_percent` | pass |
| `resume_continuity_100_percent` | pass |
| `superseded_requirement_active_0` | pass |

### Privacy 與 bounded-state Gates

- Defined synthetic secret matches：`0`
- Limitation：Defined API-key, Bearer-token, password, and private-key patterns are checked; unknown secret formats are not guaranteed to be detected.
- State budget violations：`0`
- Failed-candidate corruptions：`0`

### Latency

- Hook p50 / worst：`10.801` / `24.460` ms
- Background p50 / worst：`14.951` / `30.280` ms

### Deviations

- None.
