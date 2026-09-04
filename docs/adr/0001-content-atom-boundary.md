# ADR 0001: ContentAtom Boundary

- 状态：Accepted
- 日期：2026-09-04
- 范围：Research、Judgment、内容生产与分发

## 决策

采用以下长期依赖方向：

```text
Research
  -> Evidence / Contradiction / Historical Context
  -> Judgment
  -> Content Thesis
  -> ContentAtom
  -> Content Distribution Pipeline
  -> Content Production / Render / Publish / Analytics
```

`ContentAtom` 是 Judgment 层和分发层之间的正式边界。它表达一个可分发的内容主张、受众、角度、证据引用、反方观点、洞察和期望行动；分发层只消费这个边界上的内容，不依赖 Judgment 的内部推理结构。

## 约束

1. Judgment 层不直接依赖具体的视频 workflow、renderer 或 publisher。
2. 任何 Agent 都不得绕过 Content Distribution Pipeline，直接调用平台生成器或 Publisher。
3. Distribution Pipeline 负责内容表达、标题/Hook/封面与脚本变体、分镜、素材、配音、字幕、QA、渲染、平台适配、发布和指标回收；不负责 Research、Evidence 判断、Contradiction、Historical Analysis 或最终 Judgment。
4. Offering 必须继续兼容 `product`、`service`、`capability` 三类输入。迁移期间由 Legacy Offering Adapter 将旧请求转换为分发层可消费的内容输入。
5. 新增平台或格式只能扩展分发层适配器，不应修改 Research 或 Judgment。

## P0 范围

P0 只记录边界，不定义正式的 `ContentAtom` Go 类型，不接入 tRPC-Agent-Go、LLM 或多 Agent 运行时，也不重命名现有 `internal/workflow`。当前确定性 workflow、渲染、A/B 变体、平台适配和发布行为保持不变。

后续迁移通过外层 `Content Distribution Pipeline` facade 承接现有视频链路：先完成 ContentAtom/Legacy Offering Adapter，再把现有 workflow 作为内部实现接入 facade。这样 Judgment 的演进不会影响既有视频生产和发布能力。

## 后果

- 当前代码仍是确定性 agentkit 占位实现，不能宣称已经运行真实 tRPC-Agent-Go。
- P1/P2 需要先补可恢复的持久化和正式领域契约，再实现 Judgment 与 ContentAtom 生产。
- 现有视频链路可以继续稳定交付，同时为 YouTube、Podcast、Newsletter、X 以及更多平台保留扩展位置。
