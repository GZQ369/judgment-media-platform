# 运行和 Smoke 验证

本文记录如何在当前 Windows / Git Bash 环境里启动视频工厂并验证主路径。

## 1. 准备环境

推荐使用本机便携 Go：

```bash
export PATH=/d/agent/.tools/go/bin:$PATH
cd /d/agent/judgment-media-platform
```

确认 Go：

```bash
go version
```

## 2. 静态检查

```bash
gofmt -w ./cmd ./internal
bash -n smoke/run_batch_smoke.sh
go test ./...
```

说明：

- `gofmt` 会修改 Go 文件，只在准备提交或确认代码格式时运行。
- `bash -n` 只检查脚本语法，不会发请求。
- `go test ./...` 会运行所有 Go 测试。

## 3. 启动服务

```bash
go run ./cmd/server
```

默认监听：

```text
http://127.0.0.1:8088
```

另开一个终端验证：

```bash
curl -sS http://127.0.0.1:8088/health
```

期望看到：

```json
{"service":"judgment-media-platform","status":"ok",...}
```

## 4. 跑批量 smoke

```bash
bash smoke/run_batch_smoke.sh
```

脚本会：

1. 检查 `/health`。
2. 读取 `smoke/batch_products_request.json`。
3. POST `/api/v1/batches/run`。
4. 打印 batch id、report path、project id。
5. 验证返回中有 report path 和 item。

PowerShell 版本：

```powershell
powershell -ExecutionPolicy Bypass -File smoke/run_batch_smoke.ps1
```

## 5. 手工 curl 验证

```bash
curl -sS http://127.0.0.1:8088/api/v1/templates
```

创建项目：

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/projects \
  -H 'Content-Type: application/json' \
  -d '{"platform":"xiaohongshu","product":{"title":"宠物自动饮水机","selling_points":["循环活水","低噪音"],"target_audience":"养猫家庭"}}'
```

批量运行：

```bash
curl -sS -X POST http://127.0.0.1:8088/api/v1/batches/run \
  -H 'Content-Type: application/json' \
  --data-binary @smoke/batch_products_request.json
```

## 6. 检查产物

Batch 成功后检查：

```text
data/batches/<batch_id>/batch_report.md
data/batches/<batch_id>/batch_report.json
data/batches/<batch_id>/projects/<project_id>/publish_package.md
data/batches/<batch_id>/projects/<project_id>/publish_package.json
```

重点看：

- `batch_report.md` 是否写清每个商品状态和下一步建议。
- `publish_package.md` 是否包含标题、封面、脚本、分镜、素材计划、caption、QA、变体。
- 中文是否乱码。
- 小红书 / 抖音是否有平台差异。

## 7. 常见问题

### 7.1 go: command not found

执行：

```bash
export PATH=/d/agent/.tools/go/bin:$PATH
```

或使用系统 Go：

```bash
export PATH="$PATH:/c/Program Files/Go/bin"
```

### 7.2 端口 8088 被占用

先确认是否已有服务在跑。不要盲目 kill 进程；如果需要结束进程，先确认 PID 和用途。

### 7.3 smoke 脚本 JSON 解析失败

Bash 脚本会优先找 `python` / `python3`，没有时回退 PowerShell。如果仍失败，可以直接使用 PowerShell smoke 脚本。

### 7.4 render=true 失败

这是可接受的增强路径失败。先确认 render=false 发布包是否已生成。OpenMontage 路径和 runner 见：

```text
D:/agent/video-workers/OpenMontage
D:/agent/video-workers/OpenMontage/video_factory_pipeline_runner.py
```

### 7.5 服务重启后查不到 batch

旧 project / batch store 是内存实现；重启后旧 API 中的 batch 列表会清空，但已导出的文件仍在 `data/batches/`。新的 `/api/v1/runs` 使用 `data/runs/runs.sqlite` 持久化 runs、events 和 artifacts。两套存储并存，不能把 Run 的持久化能力推广成旧 batch API 已持久化。

## 8. 验收记录模板

每次验证可以记录：

```text
日期：
分支/目录：D:/agent/judgment-media-platform
命令：
- gofmt -w ./cmd ./internal
- bash -n smoke/run_batch_smoke.sh
- go test ./...
- go run ./cmd/server
- bash smoke/run_batch_smoke.sh

结果：
- go test：通过/失败，关键输出：...
- HTTP smoke：通过/失败，batch id：...
- 产物路径：...
- 阻塞项：...
```
