# agentcomm 评测报告

- 生成时间：2026-10-15T08:02:27Z
- 提交：`380819b45020d9fa1e8eba93a3dd5e9902527627`
- 环境：embedding=mock-1024 llm=mock judge=mock digest=`f668b7beff182e16`

## e2e（40 例）

| 指标 | 值 |
|---|---|
| answer_contains_rate | 1.0000 |
| avg_input_tokens | 0.0000 |
| avg_output_tokens | 9.3000 |
| intent_accuracy | 1.0000 |
| state_correct_rate | 1.0000 |
| success_rate | 1.0000 |
| tool_trace_match_rate | 1.0000 |

## router（100 例）

| 指标 | 值 |
|---|---|
| hit_at_1 | 0.6182 |
| mrr | 0.7308 |
| precision_at_5 | 0.4072 |
| recall_at_5 | 0.9200 |
| token_saved_pct_avg | 72.4733 |

### 失败用例（9）

- **r-dist-019** `read the file then calculate the average of the numbers inside`：recall@5=0.50（相关 [fs/read_file math/calculator]，实选 [fs/read_file fs/write_file dev/code_review net/get_weather math/add]）
- **r-dist-020** `把我搜索收集的资料整理进 report.docx`：recall@5=0.00（相关 [fs/write_file]，实选 [web/search math/sqrt math/add web/fetch_url]）
- **r-file-006** `读一下 main.go，把里面的TODO都整理到 todo.md`：recall@5=0.00（相关 [fs/read_file fs/write_file]，实选 [web/fetch_url net/get_news math/calculator]）
- **r-file-007** `查看 /var/log/syslog 最后 100 行`：recall@5=0.00（相关 [fs/read_file]，实选 [math/add math/calculator math/sqrt]）；top1=math/add 期望 fs/read_file
- **r-web-003** `打开 https://example.com 看看内容`：recall@5=0.00（相关 [web/fetch_url]，实选 [math/add math/calculator math/sqrt]）；top1=math/add 期望 web/fetch_url
- **r-web-005** `帮我查一下 MCP 协议是什么`：recall@5=0.00（相关 [web/search]，实选 [web/fetch_url]）
- **r-web-007** `2026 年 A2A 规范有哪些更新`：recall@5=0.00（相关 [web/search]，实选 [net/get_weather dev/code_review dev/format_code dev/run_tests]）
- **r-web-009** `latest news about AI agents`：recall@5=0.50（相关 [web/search net/get_news]，实选 [net/get_news dev/code_review dev/format_code dev/run_tests]）
- **r-web-011** `download the content of that link`：recall@5=0.00（相关 [web/fetch_url]，实选 [dev/run_tests fs/read_file fs/write_file net/get_news]）

