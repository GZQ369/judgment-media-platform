# Tools 模块

## 模块职责

`internal/tools` 承载 workflow 可复用工具，包括商品输入规整、平台模板、素材缺口、字幕切分和 QA 检查。

## 关键文件

- `internal/tools/product.go`
- `internal/tools/template.go`
- `internal/tools/asset.go`
- `internal/tools/subtitle.go`
- `internal/tools/qa.go`

## 输入 / 输出

输入是商品、平台、脚本、素材路径等轻量数据。输出是规整后的商品、平台 profile、素材建议、字幕行和 `domain.QAReport`。

## 调用链路

workflow 节点直接调用 tools。Engine 不直接处理平台文案细节，避免把业务规则散落在应用服务层。

## 与 tRPC-Agent-Go 的关系

这些函数对应未来 GraphAgent 的 Tool calling，例如 `parse_product_input`、`load_template`、`mark_missing_assets`、`generate_srt`、`check_platform_style`。

## 无 API key fallback

tools 都是确定性函数。没有商品卖点、痛点、人群或素材时会补默认值，保证发布包可生成。

## 测试方法

运行：

```bash
go test ./internal/workflow ./internal/engine
```

平台差异化由 `TestPlatformSpecificBriefDiffers` 覆盖。

## 常见失败与排查

- 标题风格像同一套模板：检查 `ProfileForPlatform`。
- QA 永远 passed：检查 `CheckPlatformStyle` 是否覆盖标题数、脚本数、CTA 和风险词。
