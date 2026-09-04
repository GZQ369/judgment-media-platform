# OpenMontage 能力映射

本文记录本项目如何借鉴 OpenMontage 的 agentic video production 思路，并垂直化到电商短视频矩阵运营。

参考项目：

- OpenMontage: https://github.com/calesthio/OpenMontage

## OpenMontage 关键启发

OpenMontage 的 README 明确定位为 open-source agentic video production system。它不是单纯文生视频，而是让 agent 处理研究、脚本、素材生成/检索、配音、字幕、剪辑和最终合成。

对本项目最有价值的点：

1. Pipeline-driven production
   - 视频生产必须先选 pipeline，再执行阶段。
   - 不同视频类型对应不同 pipeline。

2. Production plan first
   - 先做概念、脚本、scene plan、工具路径和成本估计，再进入生成。

3. Tool registry / capability envelope
   - 先知道当前机器有哪些工具、provider、素材能力，再承诺输出。

4. Human approval
   - 创意和生产关键点需要确认，不应该盲目全自动。

5. Validation
   - 产物需要 ffprobe、帧采样、音频、字幕、交付承诺等检查。

6. Multiple render paths
   - OpenMontage 使用 Remotion / HyperFrames / FFmpeg 等组合。
   - 本项目 MVP 先用 FFmpeg，后续接 Remotion 或 HTML 动效渲染。

## 垂直化方向

OpenMontage 面向通用视频生产，本项目面向短视频电商矩阵运营。

| OpenMontage 通用能力 | 本项目垂直化能力 |
| --- | --- |
| prompt / reference video | 商品信息 / 商品链接 / 商品素材 / 参考爆款视频 |
| research | 卖点、人群、痛点、场景分析 |
| concept | 内容角度：痛点、种草、测评、避坑、低价好物 |
| script | 小红书 / 抖音 / 视频号短视频脚本 |
| scene plan | 9:16 短视频分镜表 |
| asset generation/retrieval | 本地素材库、商品图、B-roll、AI 图、补拍清单 |
| voiceover | Edge TTS / 本地 TTS 中文配音 |
| captions | SRT / ASS / 平台字幕样式 |
| composition | FFmpeg / Remotion / HTML 动效渲染 |
| validation | 平台适配、禁用词、时长、字幕、素材缺失检查 |
| final video | MP4 + 标题 + 封面文案 + 正文 + 标签 + 发布包 |

## 本项目 Pipeline 类型

### 1. 商品种草视频 Product Seed Video

输入商品信息，输出小红书/抖音/视频号发布包。

MVP 优先级最高。

### 2. 工具测评视频 Tool Review Video

输入 SaaS/工具/软件产品信息，输出测评类视频脚本和分镜。

适合 B2B 营销团队、知识博主。

### 3. 企业案例视频 Case Study Video

输入客户案例和业务数据，输出企业案例短视频。

适合 B2B、咨询、服务商。

### 4. 知识内容视频 Knowledge Clip

输入知识点、文章、课程片段，输出知识短视频。

适合知识付费和个人 IP。

## MVP Pipeline：xiaohongshu_product_seed

### 输入

```json
{
  "platform": "xiaohongshu",
  "template": "xiaohongshu_product_seed",
  "duration_seconds": 45,
  "product": {
    "title": "宠物自动饮水机",
    "link": "https://example.com/product",
    "price": "89",
    "selling_points": ["静音", "过滤", "可拆洗"],
    "target_audience": "养猫新手",
    "images": ["data/assets/cat-water-1.jpg"]
  }
}
```

### 输出

```text
project/
  brief.md
  product_analysis.json
  angles.json
  scripts.json
  storyboard.json
  material_plan.json
  subtitle_plan.json
  render_plan.json
  variants.json
  export_package.md
```

## 阶段设计

```text
1. AnalyzeProduct
2. PlanAngles
3. WriteScripts
4. DirectStoryboard
5. PlanMaterials
6. PlanVoiceover
7. PlanSubtitles
8. PlanRender
9. ReviewQuality
10. GenerateVariants
11. ExportPackage
```

每个阶段都应该输出结构化 JSON，同时生成适合用户看的 Markdown。

## 质量门禁

OpenMontage 的“交付承诺验证”思想在本项目中落为这些门禁：

1. 平台门禁
   - 小红书：是否像真实笔记？是否适合收藏？标题是否不过度营销？
   - 抖音：前 3 秒是否强？节奏是否快？CTA 是否明确？
   - 视频号：是否可信、生活化、适合私域转化？

2. 电商合规门禁
   - 是否夸大功效？
   - 是否有敏感词？
   - 是否承诺医疗、收益、绝对效果？

3. 视频生产门禁
   - 总时长是否符合？
   - 字幕是否过长？
   - 每个镜头是否有素材？
   - 配音文本是否和字幕一致？

4. 发布包门禁
   - 是否有标题？
   - 是否有封面文案？
   - 是否有正文？
   - 是否有标签？
   - 是否有变体编号？

## 与 OpenMontage 的边界

本项目不在 MVP 阶段实现这些通用能力：

- 通用网页研究
- 大规模开放素材搜索
- 多视频生成 provider 路由
- 复杂电影级视频生成
- 通用 reference video 风格迁移

MVP 只实现电商短视频矩阵运营最需要的垂直链路：

```text
商品输入 -> 种草脚本 -> 分镜 -> 素材计划 -> 发布包 -> 批量变体
```
